#!/usr/bin/env bash
# image/webtop-nix-reinstall.sh with Nix and its neighbours stubbed; needs bash and jq. The real thing
# on the real image: .github/scripts/lower-swap-test.sh.
set -euo pipefail
script=$(cd "$(dirname "$0")/../../image" && pwd)/webtop-nix-reinstall.sh
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
fail() { echo "FAIL  $*"; exit 1; }
ok() { echo "PASS  $*"; }

mkdir "$t/bin"
stub() { printf '#!/usr/bin/env bash\n%s\n' "$2" > "$t/bin/$1"; chmod +x "$t/bin/$1"; }
stub nix 'echo "nix $*" >> "$T/calls"; [ "${NIX_FAIL:-}" != "$5" ] || [ -z "${NIX_FAIL:-}" ] || exit 1'
stub webtop-restore-progress 'echo "progress $*" >> "$T/calls"; [ "$1" != nixconfig ] || printf %s "${NIX_CONFIG:-}"'
stub webtop-notify 'echo "notify $1" >> "$T/calls"'
stub setup 'echo setup >> "$T/calls"; [ -z "${SETUP_FAIL:-}" ] || exit 1'
export T=$t PATH=$t/bin:$PATH WEBTOP_SETUP=$t/bin/setup

# A fresh home: <live> is a store path that exists, dangling links point nowhere.
home() {
  HOME=$t/h; rm -rf "$HOME" "$HOME".* ; export HOME
  P=$HOME/.local/state/nix/profiles
  mkdir -p "$P" "$t/live"; : > "$t/calls"
  ln -s /nix/store/gone-hm "$P/home-manager-1-link"
  ln -s /nix/store/gone-decl "$P/declaration"
  ln -s "$t/live" "$P/home-manager-2-link"
}
run() { rc=0; (cd "$t" && bash -c "set -euo pipefail; source '$script'" reinstall "$@") > "$t/out" 2>&1 || rc=$?; }
cat > "$t/m3.json" <<'EOF'
{"version":3,"elements":{
  "hello":{"url":"github:x/y/abc","attrPath":"packages.x.hello","storePaths":["/nix/store/h"]},
  "tree":{"url":"github:x/y/abc","attrPath":"packages.x.tree","storePaths":["/nix/store/t"]},
  "bare":{"storePaths":["/nix/store/bare"]}}}
EOF
aside_dir() { echo "$HOME"/.local/state/nix/profiles.old-*; }

# Dangling profile: moved aside, reinstalled by locked URL into the profile, then the switch.
home
ln -s /nix/store/gone-p "$P/profile"; ln -s /nix/store/gone-p1 "$P/profile-1-link"
ln -s /nix/store/gone-p "$HOME/.nix-profile"
run "$t/m3.json"
[ "$rc" = 0 ] || { cat "$t/out"; fail "exit $rc"; }
for l in home-manager-1-link declaration profile profile-1-link; do
  [ -L "$(aside_dir)/$l" ] && [ ! -e "$P/$l" ] || fail "$l was not moved aside"
done
[ -L "$P/home-manager-2-link" ] || fail "a live generation was moved"
[ ! -L "$HOME/.nix-profile" ] || fail "the dangling ~/.nix-profile stayed"
grep -qx 'nix profile add --profile-backend profile github:x/y/abc#packages.x.hello' "$t/calls" || fail "hello not reinstalled: $(cat "$t/calls")"
grep -qx 'nix profile add --profile-backend profile github:x/y/abc#packages.x.tree' "$t/calls" || fail "tree not reinstalled"
! grep -q bare "$t/calls" || fail "an element without a URL was reinstalled"
grep -q 'not reinstallable (no flake reference): /nix/store/bare' "$t/out" || fail "the bare element is not reported"
[ "$(grep -n '^setup$' "$t/calls" | cut -d: -f1)" -gt "$(grep -n 'packages.x.tree' "$t/calls" | head -n1 | cut -d: -f1)" ] || fail "the switch ran before the reinstall"
grep -qx 'progress total 3' "$t/calls" || fail "the total is not the packages plus the switch"
[ "$(grep -c '^progress step' "$t/calls")" = 3 ] || fail "not one step per package and the switch"
[ "$(tail -n 1 "$t/calls")" = 'progress finish ' ] || [ "$(tail -n 1 "$t/calls")" = 'progress finish' ] || fail "no plain finish last: $(tail -n 1 "$t/calls")"
! grep -q '^notify' "$t/calls" || fail "a notification without a failure"
ok "dangling links aside, packages by locked URL, then the switch"

# The profile came back: nothing to reinstall, only the switch.
home
ln -s "$t/live" "$P/profile"
run "$t/m3.json"
[ "$rc" = 0 ] || fail "exit $rc"
! grep -q '^nix ' "$t/calls" || fail "a restored profile was reinstalled"
[ -L "$P/profile" ] || fail "the restored profile moved"
grep -qx setup "$t/calls" || fail "no switch"
ok "a restored profile is left alone"

# No manifest: as a home with no profile.
home
run "$t/none.json"
[ "$rc" = 0 ] || fail "exit $rc without a manifest"
! grep -q '^nix ' "$t/calls" || fail "reinstalled without a manifest"
grep -qx setup "$t/calls" || fail "no switch without a manifest"
ok "no manifest: only the switch"

# Manifest v2: an array.
home
echo '{"version":2,"elements":[{"url":"github:x/y/abc","attrPath":"packages.x.old"}]}' > "$t/m2.json"
run "$t/m2.json"
grep -q 'github:x/y/abc#packages.x.old' "$t/calls" || fail "the v2 manifest was not read"
ok "manifest v2"

# A package fails: the rest still go, the switch runs, and the failure is reported for a retry.
home
export NIX_FAIL=github:x/y/abc#packages.x.hello
run "$t/m3.json"
unset NIX_FAIL
[ "$rc" = 1 ] || fail "a failed package exited $rc"
grep -q 'packages.x.tree' "$t/calls" || fail "a failure stopped the other packages"
grep -qx setup "$t/calls" || fail "a failure stopped the switch"
grep -q '^progress finish Some of your software' "$t/calls" || fail "the failure is not in the progress"
grep -q '^notify Package reinstall incomplete' "$t/calls" || fail "no notification"
ok "a failed package: the rest continue, exit 1"

# The switch fails.
home
export SETUP_FAIL=1
run "$t/none.json"
unset SETUP_FAIL
[ "$rc" = 1 ] || fail "a failed switch exited $rc"
grep -q '^progress finish Some of your software' "$t/calls" || fail "the failure is not in the progress"
ok "a failed switch: exit 1"
