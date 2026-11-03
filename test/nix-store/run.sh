#!/bin/sh
# Runs the store tests in privileged containers (overlay mounts). Needs docker and jq.
#   restore-test.sh  webtop-nix-restore.sh after an image update
#   partial-test.sh  webtop-nix-lower / webtop-nix-partial (the overlay kept across an image update)
set -e
cd "$(dirname "$0")/../.."
rev=$(jq -r '.nodes[.nodes.root.inputs.nixpkgs].locked.rev' flake.lock)
for t in ${*:-restore-test.sh partial-test.sh}; do
  echo "::group::$t"
  docker run --rm --privileged -e NIXPKGS_REV="$rev" -v "$PWD:/src:ro" nixos/nix:2.34.9 sh "/src/test/nix-store/$t"
  echo "::endgroup::"
done
