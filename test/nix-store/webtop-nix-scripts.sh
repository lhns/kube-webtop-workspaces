# Sourced inside the container: copies the store overlay scripts of the webtop-nix this flake locks
# (WEBTOP_NIX_REV, set by run.sh) to /wn-scripts, as they sit in modules/store-overlay/. A copy,
# because restore-test.sh mounts an overlay on /nix/store.
if [ ! -d /wn-scripts ]; then
  src=$(nix --extra-experimental-features nix-command --extra-experimental-features flakes eval --raw --impure \
    --expr "(builtins.fetchTree { type = \"github\"; owner = \"lhns\"; repo = \"webtop-nix\"; rev = \"$WEBTOP_NIX_REV\"; }).outPath")
  mkdir /wn-scripts && cp "$src"/modules/store-overlay/webtop-nix-* /wn-scripts/
fi
