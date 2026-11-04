# usage: webtop-nix-reinstall MANIFEST
# After an image update, as the user: what the restore could not bring back. Dangling generations move
# aside; the profile's flake packages are reinstalled from MANIFEST, the old profile's manifest.json;
# then home-manager relocks and switches ($WEBTOP_SETUP). Exit 0 only when all of it worked: the caller
# keeps its marker otherwise and retries next boot.
manifest=$1

# The agent shows these as a job in the tray; never fatal.
progress() { webtop-restore-progress "$@" || true; }
failed=0
progress start 1
profiles="$HOME/.local/state/nix/profiles"
old="$profiles.old-$(date +%s)"
# Dangling links (their generations were not restored) are moved aside: nix cannot extend them.
aside() {
  mkdir -p "$old"
  for l in "$@"; do
    if [ -L "$l" ]; then mv "$l" "$old/"; fi
  done
}

# A generation whose store path did not come back cannot be rolled back to: every dangling
# one moves aside (nix profile, home-manager, the declaration's history, ADR 0013).
for l in "$profiles"/*-link "$profiles/declaration"; do
  if [ -L "$l" ] && [ ! -e "$l" ]; then aside "$l"; fi
done

p="$profiles/profile"
if [ -L "$p" ] && [ -e "$p" ]; then
  echo "nix profile restored"
else
  if [ -L "$p" ]; then
    aside "$p" "$profiles"/profile-*-link
  fi
  if [ -L "$HOME/.nix-profile" ] && [ ! -e "$HOME/.nix-profile" ]; then rm -f "$HOME/.nix-profile"; fi
  if [ -f "$manifest" ]; then
    # Manifest v2 has an array of elements, v3 a map. Installed by locked URL: same versions.
    refs=$(jq -r '.elements | to_entries[] | .value | select(.url != null and .attrPath != null)
                  | "\(.url)#\(.attrPath)"' "$manifest")
    # Into the profile, where they were: declared packages come back with the switch below.
    # shellcheck disable=SC2086
    progress total $(( $(echo $refs | wc -w) + 1 ))
    for ref in $refs; do
      echo "reinstalling $ref"
      progress current "Reinstalling $ref"
      NIX_CONFIG=$(progress nixconfig) nix profile add --profile-backend profile "$ref" || { echo "failed: $ref"; failed=1; }
      progress step
    done
    jq -r '.elements | to_entries[] | .value | select(.url == null) | .storePaths[]?' "$manifest" \
      | sed 's/^/not reinstallable (no flake reference): /'
  fi
fi

# home-manager: relock, switch.
"$WEBTOP_SETUP" || failed=1
progress step

if [ "$failed" = 0 ]; then
  progress finish
else
  progress finish "Some of your software could not be restored; see: journalctl -u webtop-nix-reinstall"
  webtop-notify "Package reinstall incomplete" "See: journalctl -u webtop-nix-reinstall" system-software-update &
fi
wait
exit "$failed"
