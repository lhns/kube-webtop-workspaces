# Brings the user's home-manager setup to the image's (README, "Software"), after
# webtop-home-manager-template.service: imports the `nix profile` flake packages into packages.json
# once that unit has added it (ADR 0013), relocks the image-registry inputs an image update left
# stale, and switches when any of that, or the template unit, changed something, or the active
# generation did not survive the update.

hm=$HOME/.config/home-manager
state=${XDG_STATE_HOME:-$HOME/.local/state}
gen=$state/nix/profiles/home-manager
import_marker=$state/webtop/packages-json-import
switch_marker=$state/webtop/home-manager-switch
switch=
importing=

# The notifications run in the background; the unit's cgroup ends with this script.
trap wait EXIT
notify() {
  # Best effort and in the background: Plasma's notification server may not be up yet.
  (for _ in $(seq 1 12); do
    timeout 5 notify-send -a Desktop -i system-software-install "$1" "$2" 2>/dev/null && exit
    sleep 5
  done) &
}

if [ -e "$switch_marker" ]; then switch=1; fi

# The profile's flake packages, by the fields `nix profile` records; store paths stay there.
if [ -e "$import_marker" ] && [ -f "$hm/packages.json" ]; then
  echo "importing the nix profile's flake packages into packages.json"
  before=$state/webtop/packages.json.before-import
  cp -f "$hm/packages.json" "$before"
  profile_json=$(nix profile list --json --profile-backend profile)
  imported=$(jq '.elements | with_entries(select((.value.url // "") != "")
                 | .value |= {originalUrl, url, attrPath, outputs, priority})' <<<"$profile_json")
  mapfile -t moved < <(jq -r 'keys[]' <<<"$imported")
  # Declared entries of the same name win.
  jq --argjson new "$imported" '.elements = ($new + .elements)' "$before" > "$hm/packages.json"
  if git -C "$hm" rev-parse --git-dir >/dev/null 2>&1; then
    # A git-ignored file stays unseen: the switch fails and the import is undone below.
    git -C "$hm" add --intent-to-add -- packages.nix packages.json || true
  fi
  profile_gen=$(readlink "$state/nix/profiles/profile" || true)
  if [ "${#moved[@]}" -gt 0 ] && ! nix profile remove --profile-backend profile "${moved[@]}"; then
    cp -f "$before" "$hm/packages.json"
    echo "removing the imported packages from the nix profile failed; the import is retried next boot"
    exit 1
  fi
  importing=1
  switch=1
fi

# Root inputs resolved through the image's registry are locked to the old image's store paths;
# relock those that are no longer valid with the locked content (a path that merely exists may not be
# a valid store path, or hold other bytes).
if [ -f "$hm/flake.lock" ]; then
  stale=()
  while IFS=$'\t' read -r name path hash; do
    h=$(nix-store -q --hash "$path" 2>/dev/null) && h=$(nix hash convert --hash-algo sha256 --to sri "$h") || h=
    if [ "$h" != "$hash" ]; then stale+=("$name"); fi
  done < <(jq -r '. as $l | .nodes.root.inputs | to_entries[] | select(.value | type == "string")
                  | .key as $k | $l.nodes[.value]
                  | select(.original.type == "indirect" and .locked.type == "path")
                  | [$k, .locked.path, (.locked.narHash // "")] | @tsv' "$hm/flake.lock")
  if [ "${#stale[@]}" -gt 0 ]; then
    echo "relocking stale inputs: ${stale[*]}"
    nix flake update --flake "$hm" "${stale[@]}"
    switch=1
  fi
fi

# Dangling generation links: an image update did not bring those generations back. None can be
# rolled back to; a dangling current one needs a switch.
old="$state/nix/profiles.old-$(date +%s)" # as webtop-nix-reinstall names it
for l in "$gen" "$gen"-*-link; do
  if [ -L "$l" ] && [ ! -e "$l" ]; then
    mkdir -p "$old" && mv "$l" "$old/"
    if [ "$l" = "$gen" ]; then switch=1; fi
  fi
done

if [ -z "$switch" ] || [ ! -f "$hm/flake.nix" ]; then exit 0; fi
rc=0
# A flake still written for the account's old name `user` (ADR 0011): switch that config as it
# expects, through the /home/user mount, until the user updates it.
has=$(nix eval --json "$hm#homeConfigurations" --apply "c: builtins.hasAttr \"$USER\" c" 2>/dev/null || echo true)
if [ "$has" = false ] && [ -d /home/user ] &&
   [ "$(nix eval --json "$hm#homeConfigurations" --apply 'c: builtins.hasAttr "user" c' 2>/dev/null)" = true ]; then
  echo "no homeConfigurations.\"$USER\" in $hm: switching the one for user (HOME=/home/user)"
  USER=user HOME=/home/user home-manager switch --flake "$hm#user" || rc=$?
  notify "Update your home-manager flake" "It still names user. See: journalctl -u webtop-home-manager-template"
else
  echo "home-manager switch --flake $hm"
  home-manager switch --flake "$hm" || rc=$?
fi
if [ "$rc" = 0 ]; then
  rm -f "$switch_marker" "$import_marker" "$state/webtop/packages.json.before-import"
  if [ -n "$importing" ] && [ "${#moved[@]}" -gt 0 ]; then
    notify "Software installation" "Your ${#moved[@]} nix profile package(s) are now declared in ~/.config/home-manager/packages.json; nix profile edits that file."
  fi
  exit 0
fi
echo "home-manager switch failed (exit $rc)"
if [ -n "$importing" ]; then
  echo "restoring packages.json and the nix profile; the import is retried next boot"
  cp -f "$before" "$hm/packages.json"
  if [ -n "$profile_gen" ]; then ln -sfn "$profile_gen" "$state/nix/profiles/profile"; fi
  notify "Software installation" "Moving your packages to packages.json failed and was undone; see: journalctl -u webtop-home-manager"
else
  notify "home-manager switch failed" "See: journalctl -u webtop-home-manager"
fi
exit "$rc"
