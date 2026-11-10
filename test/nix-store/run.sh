#!/bin/sh
# Runs the store tests in privileged containers (overlay mounts). Needs docker and jq.
# They exercise webtop-nix's store overlay scripts (its ADR 0013) at the locked revision:
#   restore-test.sh  webtop-nix-restore.sh after an image update
#   partial-test.sh  webtop-nix-lower / webtop-nix-partial (the overlay kept across an image update)
set -e
cd "$(dirname "$0")/../.."
# nixpkgs follows webtop-nix's: root -> webtop-nix -> its nixpkgs node.
rev=$(jq -r '.nodes[.nodes[.nodes.root.inputs.nixpkgs[0]].inputs.nixpkgs].locked.rev' flake.lock)
# The scripts under test are webtop-nix's, at the revision the flake locks.
wnrev=$(jq -r '.nodes["webtop-nix"].locked.rev' flake.lock)
for t in ${*:-restore-test.sh partial-test.sh}; do
  echo "::group::$t"
  docker run --rm --privileged -e NIXPKGS_REV="$rev" -e WEBTOP_NIX_REV="$wnrev" -v "$PWD:/src:ro" nixos/nix:2.34.9 "/src/test/nix-store/$t"
  echo "::endgroup::"
done
