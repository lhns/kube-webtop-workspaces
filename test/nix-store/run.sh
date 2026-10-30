#!/bin/sh
# Runs restore-test.sh in a privileged container (overlay mounts). Needs docker.
set -e
cd "$(dirname "$0")/../.."
exec docker run --rm --privileged -v "$PWD:/src:ro" nixos/nix:2.34.9 bash /src/test/nix-store/restore-test.sh
