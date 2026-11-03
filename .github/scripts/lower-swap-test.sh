#!/usr/bin/env bash
# The store overlay across an image update, end to end with the real image (webtop-init,
# webtop-nix-lower/-partial under the image's busybox and Nix, the restore unit). Two boots on one
# volume; between them the volume is edited offline so that, for the second boot, a path the upper
# depends on looks like it was in the old image's store and is gone from the new one (the second
# image would otherwise be a second multi-GB build). Also checks the environment the partial
# invalidation relies on: overlay module parameters, the mount options inside a user namespace, and
# that every path in the image's DB exists in its store.
#
# Usage: lower-swap-test.sh <image> <timeout-seconds-per-boot>
set -euo pipefail

image=$1
timeout=$2
dir=$(mktemp -d)
mkdir "$dir/persist"
c=desktop-swap
fail() { echo "::error::$*"; exit 1; }
x() { docker exec "$c" "$@"; }

echo "overlay module parameters:"
grep -r . /sys/module/overlay/parameters/ 2>/dev/null | sed 's|/sys/module/overlay/parameters/|  |' || echo "  (none readable)"

boot() {
  docker rm -f "$c" >/dev/null 2>&1 || true
  docker run -d --name "$c" --privileged --cgroupns=private --shm-size=1g -v "$dir/persist:/persist" \
    -e WEBTOP_DESKTOP=ci -e WEBTOP_USER=ci -e WEBTOP_CONTROLLER_URL=http://127.0.0.1:9 "$image" >/dev/null
  local deadline=$(($(date +%s) + timeout))
  until x nix-store -q --hash /run/current-system >/dev/null 2>&1; do
    [ "$(docker inspect -f '{{.State.Running}}' "$c")" = true ] || { docker logs "$c" 2>&1 | tail -n 50; fail "the container exited"; }
    [ "$(date +%s)" -lt "$deadline" ] || { docker logs "$c" 2>&1 | tail -n 50; fail "no nix-daemon within ${timeout}s"; }
    sleep 2
  done
}
# Not waiting for the power hook's report to the (absent) controller; webtop-init must cope anyway.
stop() { docker stop -t 15 "$c" >/dev/null; }
heads='BEGIN{st=0} { if (st==0) {print; st=1} else if (st<4) st++; else if (st==4) { n=$0+0; st = n ? 5 : 0 } else if (--n == 0) st=0 }'
nixexpr() { # <name> <shell command writing $out>
  printf 'derivation { name = "%s"; system = builtins.currentSystem; builder = "/bin/sh"; args = [ "-c" "%s" ]; }' "$1" "$2"
}

echo "== boot 1"
boot
lower_db=$(x sh -c "nix-store --store 'local?root=/.lower&read-only=true' --dump-db | busybox awk '$heads' | sort")
missing=$(x sh -c "nix-store --store 'local?root=/.lower&read-only=true' --dump-db | busybox awk '$heads' | while read -r p; do [ -e \"/.lower\$p\" ] || echo \"\$p\"; done")
[ -z "$missing" ] || fail "paths in the image's DB missing from its store: $missing"
echo "all $(wc -l <<<"$lower_db") paths of the image's DB exist in its store"
extra=$(x sh -c 'ls -A /.lower/nix/store' | grep -vx '\.links' | sed 's|^|/nix/store/|' | grep -vxF -f <(cat <<<"$lower_db") || true)
[ -z "$extra" ] || fail "store entries not in the image's DB: $extra"
echo "and its store holds nothing else"
same_paths() {
  [ "$(sudo head -n 1 "$dir/persist/nix/lower-paths")" = "stamp $(x cat /webtop-image-stamp)" ] &&
  [ "$(sudo tail -n +2 "$dir/persist/nix/lower-paths" | sort)" = "$(sort <<<"$lower_db")" ]
}
same_paths || fail "first boot did not save the image's path list"
x unshare --user --map-root-user --mount sh -c 'mkdir -p /tmp/ovl && mount -t tmpfs tmpfs /tmp/ovl &&
  mkdir /tmp/ovl/l /tmp/ovl/u /tmp/ovl/w /tmp/ovl/m &&
  mount -t overlay overlay -o lowerdir=/tmp/ovl/l,upperdir=/tmp/ovl/u,workdir=/tmp/ovl/w,userxattr,index=off,xino=off /tmp/ovl/m &&
  grep " /tmp/ovl/m " /proc/self/mounts' || fail "the overlay options are refused inside a user namespace"
# The kernel lists index and xino only where they differ from its defaults.
m=$(x grep ' /nix/store ' /proc/self/mounts)
echo "$m"
! grep -qE 'index=on|xino=(on|auto)|metacopy=on|redirect_dir=(on|follow)' <<<"$m" || fail "/nix/store has a feature on that offline lower changes forbid"

# A lower path the user paths can depend on, and user paths built in the overlay store.
bb=$(x sh -c 'readlink -f /bin/busybox | cut -d/ -f1-4')
g=$(x nix-build --no-out-link -E "$(nixexpr ci-gone 'echo g > \$out')")
ug=$(x nix-build --out-link /nix/var/nix/gcroots/ci-ug -E "$(nixexpr ci-ug "echo \${builtins.storePath \"$g\"} > \$out")")
us=$(x nix-build --out-link /nix/var/nix/gcroots/ci-us -E "$(nixexpr ci-us "echo \${builtins.storePath \"$bb\"} > \$out")")
x nix-store --check-validity "$g" "$ug" "$us"
x nix-store -q --references "$ug" | grep -qx "$g" || fail "ug does not reference g"
stop

# Offline: g "was in the old image" (its files gone with it), and the image changed.
u=$dir/persist/nix/upper
sudo test -e "$u/${g#/nix/store/}" || fail "g is not in the upper"
sudo rm -rf "${u:?}/${g#/nix/store/}"
# What a write into a lower path through the overlay leaves: a plain upper dir with the new file.
sudo mkdir -p "$u/${bb#/nix/store/}" && sudo touch "$u/${bb#/nix/store/}/ci-delta"
echo "$g" | sudo tee -a "$dir/persist/nix/lower-paths" >/dev/null
sudo sed -i '1s/.*/stamp old-image/' "$dir/persist/nix/lower-paths"
echo old-image | sudo tee "$dir/persist/nix/lower-stamp" >/dev/null

echo "== boot 2"
boot
clog=$(docker logs "$c" 2>&1)
grep 'webtop-nix-partial\|webtop-init: .*store' <<<"$clog" || true
grep -q 'store overlay kept' <<<"$clog" || fail "the partial invalidation did not run or fell back"
grep -q '1 seeds, 3 paths to invalidate' <<<"$clog" || fail "expected 1 seed (g) and 3 invalidated paths (g, ug, and ug's .drv, which has g as an input source)"
x nix-store --check-validity "$us" || fail "us (no dependency on g) did not survive"
! x nix-store --check-validity "$ug" 2>/dev/null || fail "ug (depends on g) is still valid"
! x test -e "$bb/ci-delta" || fail "the copy-up leftover still shows"
! sudo test -e "$u/${bb#/nix/store/}" || fail "the copy-up leftover is still in the upper"
[ "$(sudo cat "$dir/persist/nix/lower-stamp")" = "$(x cat /webtop-image-stamp)" ] || fail "stamp not updated"
same_paths || fail "lower-paths is not the image's path list"
# The restore unit (markers set by the invalidation) runs and finishes; ug cannot come back (g has
# no substitute), so the reinstall stays pending.
for _ in $(seq 1 90); do sudo test -e "$dir/persist/nix/reinstall/restore" || break; sleep 2; done
! sudo test -e "$dir/persist/nix/reinstall/restore" || fail "the restore did not finish"
x journalctl -u webtop-nix-restore -o cat --no-pager | tail -n 5
x nix-store --verify --check-contents || fail "the store does not verify"
stop
docker rm -f "$c" >/dev/null
echo "store overlay across an image update: ok"
