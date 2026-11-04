# Creates the starter flake once (the marker keeps a deleted one from coming back) and, while its
# files are unmodified (webtop-template: what this unit last wrote, or the template rendered for
# another name in the home paths), keeps them on the current template and login name. Anything
# else is left alone and the user is told: README, "home-manager". Runs as the desktop user.

home=/persist/home/user
dir=$home/.config/home-manager
state=$home/.local/state/webtop
marker=$state/home-manager-template
notice=$state/home-manager-notice # first line the title, the rest the text (webtop-home-manager-notice)
seed=${WEBTOP_TEMPLATE_DIR:-/etc/webtop/home-manager}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The starter's module writes ~/.config/nix/nix.conf; one of the user's own stays theirs.
own_nix_conf() {
  local c=$home/.config/nix/nix.conf
  { [ -e "$c" ] || [ -L "$c" ]; } && ! grep -qs '^profile-backend = declaration:' "$c"
}

# hold <reason>: leave the starter as it is and tell the user, once per template version.
hold() {
  local stamp
  stamp=$(cat "$tmp/new/flake.nix" "$tmp/new/home.nix" | sha256sum | cut -d' ' -f1)
  [ "$(cat "$state/template-notified" 2>/dev/null || true)" != "$stamp" ] || exit 0
  printf '%s\n' "New home-manager starter template" \
    "Not applied: $1. See what changed: webtop-home-manager-update; --apply takes it and keeps backups." | tee "$notice"
  echo "$stamp" > "$state/template-notified" # after the notice: a stop in between notifies next boot
  exit 0
}

webtop-template render "$tmp/new" "$USER"
rm -f "$notice"
mkdir -p "$state"

if [ ! -e "$dir" ]; then
  [ ! -e "$marker" ] || exit 0
  # Staged and renamed in one step: a stop never leaves a partial starter that would count as existing.
  rm -rf "$dir.new"
  mkdir -p "$dir.new"
  cp "$tmp/new/flake.nix" "$tmp/new/home.nix" "$seed/packages.json" "$dir.new/"
  chmod 644 "$dir.new"/*
  touch "$state/home-manager-switch"
  mv -T "$dir.new" "$dir"
  # The hashes follow; until then the files equal the template, which counts as unmodified.
  webtop-template write "$dir/flake.nix" "$tmp/new/flake.nix"
  webtop-template write "$dir/home.nix" "$tmp/new/home.nix"
  webtop-template write "$dir/packages.json" "$seed/packages.json"
  touch "$marker"
  exit 0
fi
if cmp -s "$dir/flake.nix" "$tmp/new/flake.nix" && cmp -s "$dir/home.nix" "$tmp/new/home.nix"; then
  # An upgrade stopped before its last step.
  if [ -e "$dir/packages.nix" ] && webtop-template unmodified "$dir/packages.nix"; then webtop-template remove "$dir/packages.nix"; fi
  exit 0
fi

for p in ${WEBTOP_HOME_PATHS:-}; do
  n=${p#/home/}
  [ "$n" = "$USER" ] || webtop-template render "$tmp/old/$n" "$n"
done
shopt -s nullglob
# The new template counts as known: a stop between a file and its recorded hash leaves it so.
if webtop-template unmodified "$dir/flake.nix" "$tmp/new/flake.nix" "$tmp"/old/*/flake.nix &&
   webtop-template unmodified "$dir/home.nix" "$tmp/new/home.nix" "$tmp"/old/*/home.nix; then
  if own_nix_conf; then hold "you have your own ~/.config/nix/nix.conf, which the new flake's module writes"; fi
  # The previous template's flake imported packages.nix; an edited one keeps that flake.
  if [ -e "$dir/packages.nix" ] && ! webtop-template unmodified "$dir/packages.nix"; then hold "packages.nix is edited"; fi
  # This order lets a stop at any point converge next run; packages.nix goes last, the old flake needs it.
  [ -e "$dir/packages.json" ] || webtop-template write "$dir/packages.json" "$seed/packages.json"
  webtop-template write "$dir/flake.nix" "$tmp/new/flake.nix"
  webtop-template write "$dir/home.nix" "$tmp/new/home.nix"
  [ ! -e "$dir/packages.nix" ] || webtop-template remove "$dir/packages.nix"
  touch "$state/home-manager-switch"
  echo "rewrote the starter flake in ~/.config/home-manager for $USER ($HOME)"
  exit 0
fi

# Edited: point at the lines that still name another user, with their replacement.
lines=$(cd "$dir" && grep -nE 'homeConfigurations\."[^"]*"|home\.(username|homeDirectory) *= *"' flake.nix home.nix 2>/dev/null |
  grep -vF -e "\"$USER\"" -e "\"$HOME\"" || true)
[ -n "$lines" ] || hold "flake.nix or home.nix is edited"
{
  echo "Update your home-manager flake"
  echo "The flake in ~/.config/home-manager names another user; change these lines, then run home-manager switch:"
  while IFS= read -r l; do
    file=${l%%:*} num=${l#*:} old=${l#*:*:}
    num=${num%%:*} old=${old#"${old%%[! ]*}"}
    new=$(printf '%s\n' "$old" | sed -E \
      -e "s/homeConfigurations\.\"[^\"]*\"/homeConfigurations.\"$USER\"/" \
      -e "s|(home\.username *= *)\"[^\"]*\"|\1\"$USER\"|" \
      -e "s|(home\.homeDirectory *= *)\"[^\"]*\"|\1\"$HOME\"|")
    echo "  $file:$num: $old  ->  $new"
  done <<< "$lines"
} | tee "$notice"
