# Brings the user's home-manager setup to the image's (README, "Software"), after
# webtop-home-manager-template.service: relocks the image-registry inputs an image update left
# stale, and switches when that or the template unit changed something, or the active generation
# did not survive the update. WEBTOP_FLAKE is the image's `webtop` flake.

hm=$HOME/.config/home-manager
state=${XDG_STATE_HOME:-$HOME/.local/state}
gen=$state/nix/profiles/home-manager
switch_marker=$state/webtop/home-manager-switch
switch=

# The notifications run in the background; the unit's cgroup ends with this script.
trap wait EXIT
# While the reinstall unit restores the software after an image update, its job entry shows these.
progress() { webtop-restore-progress "$@" || true; }
notify() { webtop-notify "$1" "$2" "${3:-system-software-install}" & }

if [ -e "$switch_marker" ]; then switch=1; fi

# Root inputs resolved through the image's registry are locked to the old image's store paths;
# relock those that are no longer valid with the locked content (a path that merely exists may not be
# a valid store path, or hold other bytes), and `webtop` whenever it is not the image's current one:
# an old copy can survive in the store overlay and would keep the module behind.
if [ -f "$hm/flake.lock" ]; then
  stale=()
  while IFS=$'\t' read -r name id path hash; do
    h=$(nix-store -q --hash "$path" 2>/dev/null) && h=$(nix hash convert --hash-algo sha256 --to sri "$h") || h=
    if [ "$h" != "$hash" ] || { [ "$id" = webtop ] && [ "$path" != "$WEBTOP_FLAKE" ]; }; then stale+=("$name"); fi
  done < <(jq -r '. as $l | .nodes.root.inputs | to_entries[] | select(.value | type == "string")
                  | .key as $k | $l.nodes[.value]
                  | select(.original.type == "indirect" and .locked.type == "path")
                  | [$k, .original.id, .locked.path, (.locked.narHash // "")] | @tsv' "$hm/flake.lock")
  if [ "${#stale[@]}" -gt 0 ]; then
    echo "relocking stale inputs: ${stale[*]}"
    if nix flake update --flake "$hm" "${stale[@]}"; then switch=1; else echo "relocking failed; flake.lock is as it was"; fi
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
progress current "home-manager switch (downloading/building)"
echo "home-manager switch --flake $hm"
NIX_CONFIG=$(progress nixconfig) home-manager switch --flake "$hm" || rc=$?
if [ "$rc" = 0 ]; then
  rm -f "$switch_marker"
  exit 0
fi
echo "home-manager switch failed (exit $rc)"
notify "home-manager switch failed" "See: journalctl -u webtop-home-manager"
exit "$rc"
