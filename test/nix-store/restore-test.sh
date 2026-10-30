#!/usr/bin/env bash
# Inside a privileged nixos/nix container (run.sh): image/webtop-nix-restore.sh against real
# local-overlay stores. Two image stores share l1; l2 is only in the old one, l3 only in the new one.
# A "pod" on the old store builds user paths, then an update moves its overlay aside as webtop-init
# does, and the restore must bring the paths back on the new store without building anything.
set -euo pipefail

SH=$(readlink -f /bin/sh)
bins=""
for f in /root/.nix-profile/bin/*; do
  d=$(dirname "$(readlink -f "$f")")
  case ":$bins:" in *":$d:"*) ;; *) bins="$bins:$d" ;; esac
done
# The profile's links break once /nix/var/nix is rebound; use the store paths directly.
export PATH="${bins#:}:$(echo /nix/store/*-util-linux-minimal-*-bin/bin):$(echo /nix/store/*-util-linux-minimal-*-mount/bin):$(echo /nix/store/*-busybox-*/bin)"
umask 022
export NIX_CONFIG="experimental-features = nix-command local-overlay-store read-only-local-store
sandbox = false
substituters =
max-jobs = 1"

RESTORE=/src/image/webtop-nix-restore.sh
pass=0
ok()  { echo "PASS  $1"; pass=$((pass + 1)); }
die() { echo "FAIL  $1"; exit 1; }

# mk <name> <ref store paths...>: a directory output that references the given paths.
mk() {
  local n=$1 r=""; shift
  for x in "$@"; do r="$r \"$x\""; done
  nix build --impure --no-link --print-out-paths --expr "
    derivation {
      name = \"$n\"; system = builtins.currentSystem;
      builder = \"${SH}\";
      args = [ \"-c\" \"\${builtins.storePath \"$(dirname "$(dirname "$(readlink -f "$(command -v mkdir)")")")\"}/bin/mkdir \$out; echo $n \$refs > \$out/f\" ];
      refs = map builtins.storePath [ $r ];
    }"
}

# Image stores, on tmpfs: copying or hard-linking from the container's own overlayfs is slow.
L1=$(mk l1); L2=$(mk l2); L3=$(mk l3)
nix copy --to file:///subst "$L2"
for s in A B; do
  mkdir -p /lower$s && mount -t tmpfs -o mode=755 tmpfs /lower$s
  mkdir -p /lower$s/nix/var/nix
  cp -a /nix/store /lower$s/nix/store
  cp -a /nix/var/nix/db /lower$s/nix/var/nix/db
done
nix store delete --store local?root=/lowerA "$L3" >/dev/null
nix store delete --store local?root=/lowerB "$L2" >/dev/null

# boot <A|B> <cmd...>: one pod boot: overlay /nix/store, /nix/var/nix on the volume, a daemon.
cat > /boot.sh <<'EOF'
set -eu
mount --make-rprivate /
mkdir -p /.lower/nix /persist/nix/upper /persist/nix/work /persist/nix/var
mount --bind "/lower$1/nix" /.lower/nix
mount -o remount,bind,ro "/lower$1/nix" /.lower/nix
mount -t overlay overlay -o lowerdir=/.lower/nix/store,upperdir=/persist/nix/upper,workdir=/persist/nix/work,userxattr /nix/store
mount --bind /persist/nix/var /nix/var/nix
nix-daemon --store 'local-overlay://?lower-store=local%3Froot%3D%2F.lower%26read-only%3Dtrue&upper-layer=/persist/nix/upper&check-mount=true' >>/tmp/daemon.log 2>&1 &
echo $! > /tmp/daemon.pid
trap 'kill "$(cat /tmp/daemon.pid)" 2>/dev/null || true' EXIT
for _ in $(seq 50); do [ -S /nix/var/nix/daemon-socket/socket ] && break; sleep 0.2; done
export NIX_REMOTE=daemon
shift; "$@"
EOF
boot() { unshare -m bash /boot.sh "$@"; }
export -f mk
export SH

# What webtop-init does on a stamp change (the part the restore depends on).
update() {
  local t=/persist/nix/trash-$1
  mkdir -p "$t"
  mv /persist/nix/upper /persist/nix/work /persist/nix/var "$t"/
  mkdir -p /persist/nix/var
  cp -a "$t/var/gcroots" "$t/var/profiles" /persist/nix/var/ 2>/dev/null || true
}

H=/persist/home/user
fresh_volume() {
  mountpoint -q /persist && umount /persist
  mkdir -p /persist && mount -t tmpfs -o mode=755 tmpfs /persist
  mkdir -p "$H/proj" "$H/.local/state/nix/profiles"
  # Pod on the old image: a profile package (needs l2), a `nix build` result (needs l1 only).
  # The daemon is killed uncleanly, as on an OOM kill.
  boot A bash -c '
    set -e
    Ua=$(mk ua '"$L1 $L2"'); Ub=$(mk ub '"$L1"'); Uc=$(mk uc "$Ub")
    nix profile add --profile '"$H"'/.local/state/nix/profiles/profile "$Ua"
    nix build --out-link '"$H"'/proj/result "$Uc"
    echo "$Ua $Ub $Uc" > /upaths
    kill -9 "$(cat /tmp/daemon.pid)"' >/dev/null 2>&1
  read -r Ua Ub Uc < /upaths
}

# Restore on the new image. max-jobs = 0: anything that would need a build fails instead.
restore() {
  boot B env NIX_CONFIG="$NIX_CONFIG
max-jobs = 0" WEBTOP_HOMES=/persist/home bash -c "$1"
}

echo "== 1: update, l2 substitutable"
fresh_volume
update 1000000001
restore "NIX_CONFIG=\"\$NIX_CONFIG
substituters = file:///subst
require-sigs = false\" bash $RESTORE; echo rc=\$? > /rc
  cat $H/.local/state/nix/profiles/profile/f > /out1; cat $H/proj/result/f > /out2
  nix-store --check-validity $Ua $Ub $Uc $L2 && echo valid > /out3
  nix store gc >/dev/null 2>&1; nix-store --check-validity $Ua $Uc && echo rooted > /out5" || true
[ "$(cat /rc)" = rc=0 ] || die "restore exit: $(cat /rc)"; ok "restore exits 0"
grep -q "^ua $L1 $L2" /out1 || die "profile content"; ok "profile restored without a build"
grep -q "^uc $Ub" /out2 || die "result content"; ok "nix build result restored"
grep -q valid /out3 || die "validity"; ok "all paths valid, l2 substituted"
grep -q rooted /out5 || die "gc removed restored paths"; ok "restored paths survive a GC (roots carried over)"

echo "== 2: update, l2 not substitutable"
fresh_volume
update 1000000001
rm -f /rc
restore "bash $RESTORE; echo rc=\$? > /rc; nix-store --check-validity $Uc && echo uc > /out1; nix-store --check-validity $Ua 2>/dev/null || echo noua > /out2" || true
[ "$(cat /rc)" = rc=1 ] || die "restore exit: $(cat /rc)"; ok "restore exits 1 (partial)"
grep -q uc /out1 || die "uc not restored"; ok "roots not needing l2 restored"
grep -q noua /out2 || die "ua should be missing"; ok "root needing l2 left to the fallback"

echo "== 3: not enough space"
fresh_volume
update 1000000001
restore "WEBTOP_NIX_RESTORE_RESERVE_KIB=999999999999 bash $RESTORE; echo rc=\$? > /rc; nix-store --check-validity $Uc 2>/dev/null || echo none > /out1" || true
[ "$(cat /rc)" = rc=2 ] || die "restore exit: $(cat /rc)"; ok "restore exits 2 when space is short"
grep -q none /out1 || die "copied despite no space"; ok "nothing copied when space is short"

echo "== 4: second update before the restore ran"
fresh_volume
update 1000000001
boot B true
update 1000000002
restore "NIX_CONFIG=\"\$NIX_CONFIG
substituters = file:///subst
require-sigs = false\" bash $RESTORE; echo rc=\$? > /rc; cat $H/.local/state/nix/profiles/profile/f > /out1" || true
[ "$(cat /rc)" = rc=0 ] || die "restore exit: $(cat /rc)"; ok "restore exits 0"
grep -q "^ua " /out1 || die "profile content"; ok "restored from the older overlay"

echo "$pass passed"
