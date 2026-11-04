# Compares ~/.config/home-manager/{flake.nix,home.nix} with the image's current starter template for
# this login name. Without arguments it prints the difference and changes nothing. With --apply it
# backs up each differing file as <file>.bak-<time>, installs the template (so that it counts as
# unmodified from then on) and runs home-manager switch; if that fails, files and hashes are put back.
# packages.json and local.nix are the user's and never touched.

# Under the volume's path, as webtop-template keys its hashes.
dir=/persist/home/user/.config/home-manager
state=${WEBTOP_TEMPLATE_STATE:-/persist/home/user/.local/state/webtop/templates.sha256}
user=${USER:-$(id -un)}

apply=
case ${1:-} in
  "") ;;
  --apply) apply=1 ;;
  *) echo "usage: webtop-home-manager-update [--apply]" >&2; exit 2 ;;
esac
[ $# -le 1 ] || { echo "usage: webtop-home-manager-update [--apply]" >&2; exit 2; }
[ "$(id -u)" != 0 ] || { echo "run as the desktop user: root would own the files it writes" >&2; exit 1; }
[ -d "$dir" ] || { echo "no starter flake in ~/.config/home-manager" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
webtop-template render "$tmp/new" "$user"

changed=()
for f in flake.nix home.nix; do
  cmp -s "$dir/$f" "$tmp/new/$f" || changed+=("$f")
done
if [ "${#changed[@]}" = 0 ]; then echo "already the current template"; exit 0; fi

# The previous template imported it; the current one does not, so an edited one would stop applying.
if [ -e "$dir/packages.nix" ]; then
  echo "packages.nix is no longer imported by the template: move what you added to it into local.nix."
fi

if [ -z "$apply" ]; then
  for f in "${changed[@]}"; do diff -u --label "yours/$f" --label "template/$f" "$dir/$f" "$tmp/new/$f" || true; done
  echo "Nothing changed. Take the template, keeping backups: webtop-home-manager-update --apply"
  exit 0
fi

stamp=$(date +%Y%m%d-%H%M%S)
cp -p "$dir/flake.lock" "$tmp/flake.lock" 2>/dev/null || true
cp -p "$state" "$tmp/state" 2>/dev/null || true
# Atomic: a stop never leaves half a file.
put_back() { cp -p "$1" "$2.webtop-tmp" && mv "$2.webtop-tmp" "$2"; }
install() {
  local f
  for f in "${changed[@]}"; do
    if [ -e "$dir/$f" ]; then put_back "$dir/$f" "$dir/$f.bak-$stamp" || return; fi
    webtop-template write "$dir/$f" "$tmp/new/$f" || return
    echo "$f: backup in $f.bak-$stamp"
  done
  home-manager switch --flake "$HOME/.config/home-manager"
}
rc=0
install || rc=$?
[ "$rc" != 0 ] || exit 0

echo "home-manager switch failed (exit $rc): restoring your files"
for f in "${changed[@]}"; do
  if [ -e "$dir/$f.bak-$stamp" ]; then put_back "$dir/$f.bak-$stamp" "$dir/$f"; else rm -f "$dir/$f"; fi
done
restore() { if [ -e "$tmp/$1" ]; then put_back "$tmp/$1" "$2"; else rm -f "$2"; fi; }
restore state "$state"
restore flake.lock "$dir/flake.lock"
exit "$rc"
