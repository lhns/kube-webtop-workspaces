# Brings the user's home-manager setup to the image's (README, "Software"): creates the starter
# flake once, moves an earlier starter flake and its `nix profile` packages to packages.json
# (ADR 0013), relocks the image-registry inputs an image update left stale, and switches when
# any of that changed or the active generation did not survive the update.
# Set by the caller: template_dir (rendered template), template_marker, known_flakes (sha256s of
# earlier starter flake.nix files, user name replaced by @user@).

hm=$HOME/.config/home-manager
state=${XDG_STATE_HOME:-$HOME/.local/state}
gen=$state/nix/profiles/home-manager
migrated_marker=$state/webtop/packages-json
switch=
migrating=

notify() {
  # Best effort and in the background: Plasma's notification server may not be up yet.
  (for _ in $(seq 1 12); do
    timeout 5 notify-send -a Desktop -i system-software-install "$1" "$2" 2>/dev/null && exit
    sleep 5
  done) &
}

# Once: the marker keeps the template from coming back after the user deletes it.
if [ ! -e "$hm" ] && [ ! -e "$template_marker" ]; then
  mkdir -p "$hm" "$(dirname "$template_marker")"
  for f in flake.nix home.nix packages.nix packages.json; do
    cp "$template_dir/$f" "$hm/$f"
    chmod u+w "$hm/$f"
  done
  touch "$template_marker" "$migrated_marker"
  switch=1
fi

# An earlier starter flake (unmodified apart from the user name) gains packages.nix; its
# `nix profile` packages move into packages.json. Anything else is left alone, once notified.
if [ -f "$hm/flake.nix" ] && [ ! -e "$hm/packages.json" ] && [ ! -e "$migrated_marker" ]; then
  mkdir -p "$(dirname "$migrated_marker")"
  attr=$(grep -oE 'homeConfigurations\."[^"]*"' "$hm/flake.nix" | head -n1 || true)
  sum=$(sed -E 's/homeConfigurations\."[^"]*"/homeConfigurations."@user@"/' "$hm/flake.nix" | sha256sum | cut -d' ' -f1)
  conf=$HOME/.config/nix/nix.conf
  if [ -n "$attr" ] && grep -qx "$sum" <<<"$known_flakes" \
    && { [ ! -e "$conf" ] || [[ "$(readlink -f "$conf")" == /nix/store/* ]]; }; then
    echo "moving the starter flake to packages.json"
    backup=$state/webtop/home-manager-before-packages-json
    rm -rf "$backup" && mkdir -p "$backup"
    cp -a "$hm/flake.nix" "$backup/"
    if [ -f "$hm/flake.lock" ]; then cp -a "$hm/flake.lock" "$backup/"; fi
    sed -E "s/homeConfigurations\.\"[^\"]*\"/${attr//\//\/}/" "$template_dir/flake.nix" > "$hm/flake.nix"
    cp "$template_dir/packages.nix" "$hm/packages.nix"
    chmod u+w "$hm/flake.nix" "$hm/packages.nix"
    # The profile's flake packages, by the fields `nix profile` records; store paths stay there.
    profile_json=$(nix profile list --json --profile-backend profile)
    jq '{version: 1, elements: (.elements | with_entries(select((.value.url // "") != "")
          | .value |= {originalUrl, url, attrPath, outputs, priority}))}' <<<"$profile_json" > "$hm/packages.json"
    mapfile -t moved < <(jq -r '.elements | keys[]' "$hm/packages.json")
    if git -C "$hm" rev-parse --git-dir >/dev/null 2>&1; then
      git -C "$hm" add --intent-to-add -- packages.nix packages.json
    fi
    profile_gen=$(readlink "$state/nix/profiles/profile" || true)
    if [ "${#moved[@]}" -gt 0 ]; then
      nix profile remove --profile-backend profile "${moved[@]}"
    fi
    migrating=1
    switch=1
  else
    notify "Software installation" "nix profile now records packages in ~/.config/home-manager/packages.json. Your home-manager setup is customized, so it was left as is; see the README, \"Software\"."
  fi
  touch "$migrated_marker"
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
  mkdir -p "$state/nix/profiles.old"
  mv "$gen" "$state/nix/profiles.old/" && for l in "$gen"-*-link; do
    if [ -L "$l" ] && [ ! -e "$l" ]; then mv "$l" "$state/nix/profiles.old/"; fi
  done
  switch=1
fi

if [ -z "$switch" ] || [ ! -f "$hm/flake.nix" ]; then exit 0; fi
echo "home-manager switch --flake $hm"
if home-manager switch --flake "$hm"; then
  if [ -n "$migrating" ]; then
    notify "Software installation" "Your ${#moved[@]} nix profile package(s) are now declared in ~/.config/home-manager/packages.json; nix profile and the software center edit that file."
  fi
  exit 0
fi
rc=$?
if [ -n "$migrating" ]; then
  echo "switch failed: restoring the previous flake and nix profile"
  cp -a "$backup/." "$hm/"
  rm -f "$hm/packages.nix" "$hm/packages.json" "$migrated_marker"
  if [ -n "$profile_gen" ]; then ln -sfn "$profile_gen" "$state/nix/profiles/profile"; fi
  notify "Software installation" "Moving your packages to packages.json failed and was undone; see: journalctl -u webtop-home-manager"
fi
exit "$rc"
