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
# Written once the packages are out of the nix profile and only in packages.json: if the unit is
# stopped before the switch, the next run only switches.
phase=$state/webtop/import-phase
before=$state/webtop/packages.json.before-import
switch=
importing=

# The notifications run in the background; the unit's cgroup ends with this script.
trap wait EXIT
# While the reinstall unit restores the software after an image update, its job entry shows these.
progress() { webtop-restore-progress "$@" || true; }
notify() { webtop-notify "$1" "$2" "${3:-system-software-install}" & }

if [ -e "$switch_marker" ]; then switch=1; fi

# The profile's flake packages, by the fields `nix profile` records; store paths stay there.
if [ -e "$import_marker" ] && [ -f "$hm/packages.json" ]; then
  if [ -f "$phase" ] && [ "$(head -n1 "$phase")" = removed ]; then
    echo "resuming the import: the packages are in packages.json already, only the switch is left"
    { read -r _; read -r profile_gen; mapfile -t moved; } < "$phase"
    trap '' TERM # as below
  else
    echo "importing the nix profile's flake packages into packages.json"
    progress current "Moving your packages to packages.json"
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
    if [ "${#moved[@]}" -gt 0 ]; then
      # Ignored until the end: a stop now would leave the packages out of both places. systemd
      # waits (TimeoutStopSec), and the phase file covers a kill.
      trap '' TERM
      if ! nix profile remove --profile-backend profile "${moved[@]}"; then
        cp -f "$before" "$hm/packages.json"
        echo "removing the imported packages from the nix profile failed; the import is retried next boot"
        exit 1
      fi
      { echo removed; echo "$profile_gen"; printf '%s\n' "${moved[@]}"; } > "$phase.tmp"
      mv -f "$phase.tmp" "$phase"
    fi
  fi
  importing=1
  switch=1
fi

# Root inputs resolved through the image's registry are locked to the old image's store paths;
# relock those that are gone.
if [ -f "$hm/flake.lock" ]; then
  stale=()
  while IFS=$'\t' read -r name path; do
    if [ ! -e "$path" ]; then stale+=("$name"); fi
  done < <(jq -r '. as $l | .nodes.root.inputs | to_entries[] | select(.value | type == "string")
                  | .key as $k | $l.nodes[.value]
                  | select(.original.type == "indirect" and .locked.type == "path")
                  | [$k, .locked.path] | @tsv' "$hm/flake.lock")
  if [ "${#stale[@]}" -gt 0 ]; then
    echo "relocking stale inputs: ${stale[*]}"
    nix flake update --flake "$hm" "${stale[@]}"
    switch=1
  fi
fi

# A dangling generation link: an image update did not bring the generation back.
if [ -L "$gen" ] && [ ! -e "$gen" ]; then
  old="$state/nix/profiles.old-$(date +%s)" # as webtop-nix-reinstall names it
  mkdir -p "$old"
  for l in "$gen" "$gen"-*-link; do
    if [ -L "$l" ] && [ ! -e "$l" ]; then mv "$l" "$old/"; fi
  done
  switch=1
fi

if [ -z "$switch" ] || [ ! -f "$hm/flake.nix" ]; then exit 0; fi
rc=0
progress current "home-manager switch (downloading/building)"
# A flake still written for the account's old name `user` (ADR 0011) is switched as it expects.
if [ "$(webtop-hm-attr "$hm")" = user ]; then
  echo "no homeConfigurations.\"$USER\" in $hm: switching the one for user (HOME=/home/user)"
  USER=user HOME=/home/user home-manager switch --flake "$hm#user" || rc=$?
  notify "Update your home-manager flake" "It still names user. See: journalctl -u webtop-home-manager-template" dialog-warning
else
  echo "home-manager switch --flake $hm"
  home-manager switch --flake "$hm" || rc=$?
fi
if [ "$rc" = 0 ]; then
  rm -f "$switch_marker" "$import_marker" "$before" "$phase"
  if [ -n "$importing" ] && [ "${#moved[@]}" -gt 0 ]; then
    notify "Software installation" "Your ${#moved[@]} nix profile package(s) are now declared in ~/.config/home-manager/packages.json; nix profile edits that file."
  fi
  exit 0
fi
echo "home-manager switch failed (exit $rc)"
if [ -n "$importing" ]; then
  echo "restoring packages.json and the nix profile; the import is retried next boot"
  if [ -f "$before" ]; then cp -f "$before" "$hm/packages.json"; fi
  if [ -n "$profile_gen" ]; then ln -sfn "$profile_gen" "$state/nix/profiles/profile"; fi
  rm -f "$phase"
  notify "Software installation" "Moving your packages to packages.json failed and was undone; see: journalctl -u webtop-home-manager"
else
  notify "home-manager switch failed" "See: journalctl -u webtop-home-manager"
fi
exit "$rc"
