#!/bin/sh
# Inside a privileged nixos/nix container (run.sh): image/webtop-nix-lower and webtop-nix-partial
# (the store overlay after an image update, ADR 0008) against real local-overlay stores, run as root
# of a user namespace with the image's static busybox, as webtop-init runs them. The restore after
# the update is image/webtop-nix-restore.sh itself.
# Needs NIXPKGS_REV (the flake's nixpkgs): busybox and sqlite3 come from it.
set -eu
[ -z "${TRACE:-}" ] || set -x
if [ ! -e /bb/awk ]; then
  f="nix --extra-experimental-features nix-command --extra-experimental-features flakes"
  bb=$($f build --no-link --print-out-paths "github:NixOS/nixpkgs/$NIXPKGS_REV#pkgsStatic.busybox")
  sq=$($f build --no-link --print-out-paths "github:NixOS/nixpkgs/$NIXPKGS_REV#sqlite.bin")
  mkdir -p /bb && "$bb/bin/busybox" --install -s /bb && ln -s "$sq/bin/sqlite3" /bb/sqlite3
  echo "$bb" > /bb/.root
fi
# The profile's coreutils stay reachable for webtop-nix-restore.sh (df --output, readlink -m).
PROFILE_PATH=${PROFILE_PATH:-$PATH}
export PATH=/bb:$PROFILE_PATH PROFILE_PATH
# The pod runs as root in a user namespace (hostUsers: false); do the same.
if [ "$(awk '{print $3}' /proc/self/uid_map)" = 4294967295 ]; then
  exec unshare --user --map-root-user --mount sh "$0" "$@"
fi
BASECONF="experimental-features = nix-command local-overlay-store read-only-local-store
sandbox = false
substitute = false
build-users-group =
require-sigs = false"
export NIX_CONFIG="$BASECONF"

ok=0; bad=0
pass() { echo "PASS: $*"; ok=$((ok + 1)); }
failt() { echo "FAIL: $*"; bad=$((bad + 1)); }
check() { if eval "$2"; then pass "$1"; else failt "$1"; fi; }

mkdir -p /t && mount -t tmpfs tmpfs /t
# The image scripts as flake.nix substitutes them.
mkdir -p /t/bin
for s in webtop-nix-lower webtop-nix-partial; do
  sed -e "s|@busybox@|$(cat /bb/.root)|" -e "s|@nixStore@|$(command -v nix-store)|" "/src/image/$s" > "/t/bin/$s"
  chmod +x "/t/bin/$s"
done
cat > /t/paths.nix <<'EOF'
let
  mk = name: script: builtins.derivation {
    inherit name; system = builtins.currentSystem; builder = "/bin/sh";
    args = [ "-c" script ]; PATH = "/bb";
  };
  a1 = mk "a1" "echo a1 > $out";
  a2 = mk "a2" "mkdir -p $out/d; echo f1 > $out/d/f1; echo f2 > $out/d/f2; echo ${a1} > $out/ref";
  a3 = mk "a3" "echo a3 > $out";
  a4 = mk "a4" "mkdir $out; echo a4 > $out/x";
  a5 = mk "a5" "mkdir -p $out/bin; echo a5 > $out/bin/tool; echo a5b > $out/bin/other";
  b1 = mk "b1" "mkdir $out; echo b1-complete-content > $out/data";
  u1 = mk "u1" "mkdir $out; echo ${a1} > $out/ref; echo common-bytes > $out/common";
  u2 = mk "u2" "mkdir $out; echo ${a3} > $out/ref";
  u2r = mk "u2r" "mkdir $out; echo ${u2} > $out/ref";
  u3 = mk "u3" "mkdir $out; echo u3 > $out/x";
  u4 = mk "u4" "mkdir $out; echo ${a2} > $out/ref";
  u5 = mk "u5" "mkdir $out; echo ${u4} > $out/ref";
  u6 = mk "u6" "mkdir $out; echo ${u1} > $out/ref; echo common-bytes > $out/common";
  u7 = mk "u7" "mkdir $out; echo ${a5} > $out/ref";
  u8 = mk "u8" "mkdir $out; echo u8 > $out/x";
in { inherit a1 a2 a3 a4 a5 b1 u1 u2 u2r u3 u4 u5 u6 u7 u8; }
EOF
for n in a1 a2 a3 a4 a5 b1 u1 u2 u2r u3 u4 u5 u6 u7 u8; do
  p=$(nix-build --no-out-link /t/paths.nix -A $n 2>/dev/null)
  eval "$n=$p"
  eval "b_$n=\${p#/nix/store/}"
done
# A derivation whose input source is a5 (dropped by B), instantiated in the overlay store.
D9EXPR="derivation { name = \"d9\"; system = builtins.currentSystem; builder = \"/bin/sh\"; PATH = \"/bb\"; args = [ \"-c\" \"echo \${builtins.storePath \"$a5\"} > \$out\" ]; }"
o9=$(nix-build --no-out-link -E "$D9EXPR" 2>/dev/null)
# What the restore can substitute: the paths only the old image had.
nix copy --no-check-sigs --to file:///t/subst "$a2" "$a5"
ROOTS="$u1 $u2r $u3 $u5 $u6 $u7 $u8 $o9"

PR=/t/p      # the volume (/persist)
S=$PR/nix
LB=/t/lowB   # the new image's /nix, bound read-only like /.lower/nix
OVL() { echo "local-overlay://?root=$S/m&state=$S/var&upper-layer=$S/upper&check-mount=true&lower-store=local%3Froot%3D$1%26read-only%3Dtrue"; }
UPS() { echo "local?real=$S/upper&state=$S/var"; }
mount_ovl() { # lower-root
  mkdir -p "$S/m/nix/store"
  mount -t overlay overlay -o "lowerdir=$1/nix/store,upperdir=$S/upper,workdir=$S/work,userxattr,index=off,xino=off" "$S/m/nix/store"
}
umount_ovl() { umount "$S/m/nix/store"; }
heads() { awk 'BEGIN{st=0} { if (st==0) {print; st=1} else if (st<4) st++; else if (st==4) { n=$0+0; st = n ? 5 : 0 } else if (--n == 0) st=0 }'; }

# SCEN=main (default) | optimise (auto-optimise-store during the session) | mirror (only a
# queried, unreferenced lower path is dropped)
setup() {
  umount "$S/m/nix/store" 2>/dev/null || true
  umount "$LB" 2>/dev/null || true
  rm -rf "$PR" /t/B; mkdir -p "$S/upper" "$S/work" "$S/var/gcroots" "$PR/home"
  if [ "${SCEN:-main}" = mirror ]; then
    nix copy --no-check-sigs --to "local?root=$PR/A" "$a1" "$a5"
    mount_ovl "$PR/A"; O=$(OVL "$PR/A")
    nix copy --no-check-sigs --to "$O" "$u1"
    nix path-info --store "$O" "$a5" >/dev/null   # mirror row, no referrer
    umount_ovl
    nix copy --no-check-sigs --to "local?root=/t/B" "$a1"
    ln -s "$u1" "$S/var/gcroots/u1"
  else
    nix copy --no-check-sigs --to "local?root=$PR/A" "$a1" "$a2" "$a3" "$a4" "$a5"
    mount_ovl "$PR/A"; O=$(OVL "$PR/A")
    if [ "${SCEN:-main}" = optimise ]; then
      NIX_CONFIG="$BASECONF
auto-optimise-store = true" nix copy --no-check-sigs --to "$O" "$u1" "$u2r" "$u3" "$u5" "$u6" "$u7" "$u8"
    else
      nix copy --no-check-sigs --to "$O" "$u1" "$u2r" "$u3" "$u5" "$u6" "$u7" "$u8"
    fi
    d9=$(nix-instantiate --store "$O" -E "$D9EXPR" 2>/dev/null)
    nix copy --no-check-sigs --to "$O" "$o9"
    M=$S/m/nix/store
    touch "$M/$b_a2/d/f1"; rm "$M/$b_a2/d/f2"   # copy-up delta incl. an inner whiteout (a2 dropped by B)
    touch "$M/$b_a5/bin/tool"                   # copy-up delta (a5 dropped by B)
    rm -rf "${M:?}/$b_a4"                       # top-level whiteout of a4 (kept by B)
    umount_ovl
    mkdir "$S/upper/$b_b1"; : > "$S/upper/$b_b1/data"   # interrupted substitution of b1 (new in B)
    rm -rf "${S:?}/upper/$b_u8"                          # a row whose files went missing
    mkdir "$S/upper/tmp-12345-0"; : > "$S/upper/$b_u1.lock"; mkdir "$S/upper/$b_u6.check"; mkdir "$S/upper/stray"
    nix copy --no-check-sigs --to "local?root=/t/B" "$a1" "$a3" "$a4" "$b1" "$u3" "$u2"
    # Same path, other bytes (a non-reproducible rebuild), registered consistently in B.
    chmod u+w "/t/B/nix/store/$b_u2"; echo rebuilt-differently >> "/t/B/nix/store/$b_u2/ref"; chmod u-w "/t/B/nix/store/$b_u2"
    printf '%s\n%s\n%s\n\n1\n%s\n' "$u2" "$(nix hash path --type sha256 --base16 "/t/B/nix/store/$b_u2")" \
      "$(nix-store --dump "/t/B/nix/store/$b_u2" | wc -c)" "$a3" |
      nix-store --store "local?root=/t/B" --register-validity --reregister --hash-given
    for r in $ROOTS; do ln -s "$r" "$S/var/gcroots/${r#*-}"; done
  fi
  # As webtop-init's bind_lower: state dirs exist before the read-only bind.
  mkdir -p /t/B/nix/var/nix/profiles/per-user /t/B/nix/var/nix/gcroots/per-user /t/B/nix/var/nix/temproots
  mkdir -p "$LB" && mount --bind /t/B "$LB" && mount -o remount,bind,ro "$LB"
  nix-store --store "local?root=$PR/A&read-only=true" --dump-db | heads | sort > "$S/lower-paths"
  echo A > "$S/lower-stamp"
}

state() { # canonical post-run state
  nix-store --store "$(UPS)" --dump-db | grep -x '/nix/store/[^ ]*' | sort -u
  echo ---; ls -A "$S/upper" | sort
  echo ---; cat "$S/lower-paths"
  echo ---; ls -A "$S"/trash-*/upper 2>/dev/null | sort
  echo ---; ls -A "$S"/trash-*/junk 2>/dev/null | sort
  echo ---; ls "$S" | grep -c '^trash-' || true
}
prestate() { # exact pre-run state: DB contents and every upper file
  nix-store --store "$(UPS)" --dump-db
  echo ---; (cd "$S/upper" && find . -exec stat -c '%n %F %s %a %t:%T' {} \; | sort)
  echo ---; ls "$S"
}

# As webtop-init runs them: no HOME and no passwd entry (before activation), busybox only.
echo B > /t/stampB
ENVP="env -u HOME -u XDG_CONFIG_HOME -u XDG_CACHE_HOME N=$S LOWER=$LB WEBTOP_NIX_PARTIAL_WORK=/t/work"
run_partial() { $ENVP PATH=/bb /t/bin/webtop-nix-partial; }
run_lower() { $ENVP STAMP_FILE=/t/stampB WEBTOP_PERSIST_ROOT=$PR PATH=/t/shim:/bb /t/bin/webtop-nix-lower; }
markers() { [ -e "$S/reinstall/pending" ] && [ -e "$S/reinstall/restore" ]; }
invalid_up() { [ "$(nix-store --store "$(UPS)" --check-validity --print-invalid "$1" 2>/dev/null)" = "$1" ]; }

# image/webtop-nix-restore.sh on the new store (the overlay over the new lower), then the valid roots.
restore_roots() {
  mkdir -p "$S/upper" "$S/work" "$S/var" # as webtop-init's prepare_persist
  mount_ovl "$LB"; O=$(OVL "$LB")
  NIX_CONFIG="$BASECONF
store = $O
substitute = true
substituters = file:///t/subst" WEBTOP_NIX_PERSIST=$S WEBTOP_HOMES=$PR/home PATH=$PROFILE_PATH \
    bash /src/image/webtop-nix-restore.sh > /t/restore.log 2>&1 || true
  for r in $ROOTS; do nix path-info --store "$O" "$r" >/dev/null 2>&1 && echo "$r"; done | sort > /t/valid-roots
  nix-store --store "$O" --verify --check-contents > /t/rv.log 2>&1 && ! grep -q 'disappeared\|modified' /t/rv.log
  rc=$?
  umount_ovl
  return $rc
}

# sync shim: records the state of lower-paths and the stamp at every sync
mkdir -p /t/shim
cat > /t/shim/sync <<EOF
#!/bb/sh
echo "sync lp=\$(md5sum < $S/lower-paths | cut -c1-8) st=\$(cat $S/lower-stamp 2>/dev/null)" >> /t/events
exec /bb/sync
EOF
chmod +x /t/shim/sync
: > /t/passwd; mount --bind /t/passwd /etc/passwd

echo "=== image update through webtop-nix-lower"
setup
old_lp=$(md5sum < "$S/lower-paths" | cut -c1-8)
check "drv registered with its DerivationOutputs row" \
  "[ \"\$(sqlite3 $S/var/db/db.sqlite \"select count(*) from DerivationOutputs where path = '$o9'\")\" = 1 ]"
: > /t/events
run_lower
new_lp=$(md5sum < "$S/lower-paths" | cut -c1-8)
check "markers set" markers
check "stamp written" "[ \"\$(cat $S/lower-stamp)\" = B ]"
check "sync before writing lower-paths" "grep -q 'lp=$old_lp st=A' /t/events"
check "sync after lower-paths, before the stamp" "grep -q 'lp=$new_lp st=A' /t/events"
check "attempt counter and partial.trash removed" "[ ! -e $S/partial.attempts ] && [ ! -e $S/partial.trash ]"
state > /t/ref.state
echo "trash/upper: $(ls -A "$S"/trash-*/upper | sort | tr '\n' ' ')"
echo "trash/junk: $(ls -A "$S"/trash-*/junk | sort | tr '\n' ' ')"
check "upper holds only kept paths (junk swept, .links re-created empty)" \
  "[ \"\$(ls -A $S/upper | sort | tr '\n' ' ')\" = \"\$(printf '%s\n' .links $b_u1 $b_u6 | sort | tr '\n' ' ')\" ] && [ -z \"\$(ls -A $S/upper/.links)\" ]"
check "workdir emptied" "[ -z \"\$(ls -A $S/work)\" ]"
for n in u2 u2r u4 u5 u7 u8 o9; do eval "p=\$$n"; check "$n invalidated" "invalid_up $p"; done
check ".drv depending on a dropped source invalidated" "invalid_up $d9"
check "DerivationOutputs row cascaded" \
  "[ \"\$(sqlite3 $S/var/db/db.sqlite \"select count(*) from DerivationOutputs where path = '$o9'\")\" = 0 ]"

mount_ovl "$LB"; O=$(OVL "$LB"); M=$S/m/nix/store
check "verify --check-contents clean" \
  "nix-store --store '$O' --verify --check-contents > /t/v.log 2>&1 && ! grep -q 'disappeared\|modified' /t/v.log"
for n in u1 u3 u6 a1 a3; do eval "p=\$$n"; check "$n valid" "nix path-info --store '$O' $p >/dev/null 2>&1"; done
check "b1 shows the lower's content" "[ \"\$(cat $M/$b_b1/data)\" = b1-complete-content ]"
check "a4 whiteout gone" "[ \"\$(cat $M/$b_a4/x)\" = a4 ]"
check "u2 shows the new lower's bytes" "grep -q rebuilt-differently $M/$b_u2/ref"
check "u3 from the lower, upper copy dropped" "[ \"\$(cat $M/$b_u3/x)\" = u3 ] && [ ! -e $S/upper/$b_u3 ]"
check "no partial dirs of removed lower paths" "[ ! -e $M/$b_a2 ] && [ ! -e $M/$b_a5 ]"
check "invalidated paths re-copied, store verifies" \
  "nix copy --no-check-sigs --to '$O' $u2r $u5 $u7 $u8 $o9 >/dev/null 2>&1 && nix-instantiate --store '$O' -E '$D9EXPR' >/dev/null 2>&1 && nix-store --store '$O' --verify --check-contents >/dev/null 2>&1"
check "gc on the result" "nix-store --store '$O' --gc >/dev/null 2>&1"
umount_ovl

echo "=== restore after an uninterrupted run"
setup; run_partial
check "webtop-nix-restore from the partial trash, store verifies" restore_roots
cp /t/valid-roots /t/ref-roots
echo "valid roots after restore: $(wc -l < /t/ref-roots)"; cat /t/restore.log
in_roots() { for r in "$@"; do grep -qx "$r" /t/ref-roots || return 1; done; }
check "restore brought back the invalidated resident roots" "in_roots $u2r $u5 $u7 $o9"

echo "=== idempotence"
setup; run_partial
before=$(state); run_partial; after=$(state)
check "second run is a no-op" "[ \"\$before\" = \"\$after\" ] && [ \"\$(cat /t/work/result)\" = clean ]"

for c in after-plan after-snapshot mid-moves after-moves after-delete after-check; do
  echo "=== kill at $c, then rerun"
  setup
  if WEBTOP_TEST_CRASH_AT=$c WEBTOP_TEST_MOVE_BATCH=1 run_partial; then failt "no crash at $c"; fi
  run_lower
  check "kill at $c: markers set" markers
  state > /t/crash.state
  if cmp -s /t/ref.state /t/crash.state; then pass "kill at $c: converges to the uninterrupted result"
  else failt "kill at $c: differs"; diff /t/ref.state /t/crash.state || true; fi
  mount_ovl "$LB"
  check "kill at $c: verifies" "nix-store --store '$(OVL "$LB")' --verify --check-contents >/dev/null 2>&1"
  umount_ovl
done

fallback_case() { # <name> <commands producing the failure>
  echo "=== $1"
  setup; prestate > /t/pre.state
  eval "$2"
  prestate > /t/post.state
  if cmp -s /t/pre.state /t/post.state; then pass "$1: pre-run state reconstructed exactly"
  else failt "$1: state differs"; diff /t/pre.state /t/post.state | head -20 || true; fi
  check "$1: no partial.* left" "! ls $S/partial.* >/dev/null 2>&1"
  # The caller's fallback: webtop-nix-lower with a failing procedure resets the overlay.
  WEBTOP_TEST_FAIL_AT=after-plan run_lower > /t/lower.log 2>&1 || true
  check "$1: the fallback is logged" "grep -q 'PARTIAL INVALIDATION FAILED' /t/lower.log"
  if restore_roots && cmp -s /t/ref-roots /t/valid-roots; then
    pass "$1: full reset + restore recover every root an uninterrupted run does"
  else
    failt "$1: full reset + restore recover every root an uninterrupted run does"
    diff /t/ref-roots /t/valid-roots || true; tail -n 20 /t/restore.log; ls "$S"
  fi
}
fallback_case "two kills, the third attempt gives up" \
  "WEBTOP_TEST_CRASH_AT=after-moves run_partial || true; WEBTOP_TEST_CRASH_AT=after-delete run_partial || true
   if run_partial; then failt 'the third attempt should give up'; fi"
fallback_case "failure after the moves" "WEBTOP_TEST_FAIL_AT=after-moves run_partial || true"
fallback_case "failure after the invalidation" "WEBTOP_TEST_FAIL_AT=after-delete run_partial || true"

echo "=== auto-optimise-store with a populated .links"
SCEN=optimise setup
check "session populated upper/.links" "[ -n \"\$(ls -A $S/upper/.links)\" ]"
run_partial
check "optimise: .links moved to junk" "[ -n \"\$(ls -A $S/trash-*/junk/.links)\" ] && [ -z \"\$(ls -A $S/upper/.links)\" ]"
mount_ovl "$LB"
check "optimise: verify clean, gc fine" "nix-store --store '$(OVL "$LB")' --verify --check-contents >/dev/null 2>&1 && nix-store --store '$(OVL "$LB")' --gc >/dev/null 2>&1 && nix-store --store '$(OVL "$LB")' --verify --check-contents >/dev/null 2>&1"
umount_ovl

echo "=== only a mirror row is affected"
SCEN=mirror setup
run_partial
check "mirror: a5 invalidated, u1 kept" "invalid_up $a5 && ! invalid_up $u1"
check "mirror: markers set, trash/upper empty" "markers && [ -z \"\$(ls -A $S/trash-*/upper)\" ]"
restore_roots || true
check "mirror: the restore has nothing to restore" "grep -q 'nothing to restore' /t/restore.log"

echo "=== first boot, and a volume from before the path list"
rm -rf "$PR"; mkdir -p "$PR"
run_lower
check "first boot: stamp and lower-paths written, no trash, no markers" \
  "[ \"\$(cat $S/lower-stamp)\" = B ] && [ -s $S/lower-paths ] && ! ls -d $S/trash-* >/dev/null 2>&1 && [ ! -e $S/reinstall ]"
rm "$S/lower-paths"; run_lower
check "same stamp: a missing lower-paths is written" "[ -s $S/lower-paths ]"
setup; rm "$S/lower-paths"
run_lower > /t/lower.log 2>&1
check "update without a path list: full reset, logged" \
  "grep -q 'no saved lower path list' /t/lower.log && grep -q 'PARTIAL INVALIDATION FAILED' /t/lower.log && [ -d $S/trash-*/var/db ] && markers && [ -s $S/lower-paths ]"

echo "RESULT: $ok passed, $bad failed"
[ "$bad" = 0 ]
