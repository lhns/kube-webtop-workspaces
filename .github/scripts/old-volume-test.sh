#!/usr/bin/env bash
# A real image update: a volume written by the previous published image, booted with this one and
# nothing edited offline (lower-swap-test.sh fakes the old image). Installed software must survive
# through the store overlay's restore.
#
# Usage: old-volume-test.sh <new-image> <previous-image> <timeout-seconds-per-boot>
# Skips (exit 0) if the previous image cannot be pulled, e.g. it has no entry for this architecture.
set -euo pipefail

image=$1
previous=$2
timeout=$3
dir=$(mktemp -d)
mkdir "$dir/persist"
c=desktop-oldvol
. "$(dirname "$0")/lower-swap-lib.sh"

docker pull -q "$previous" >/dev/null 2>&1 || { echo "skipped: cannot pull $previous"; exit 0; }

echo "== previous image: boot 1"
boot "$previous"
wait_setup
as_user nix profile add nixpkgs#hello || fail "nix profile add failed"
as_user nix profile add --profile-backend profile nixpkgs#cowsay || fail "nix profile add to the profile failed"
software_works "on the previous image"
stop

echo "== new image: boot 2"
boot
clog=$(docker logs "$c" 2>&1)
grep 'webtop-init: .*store' <<<"$clog" || true
! grep -q 'first boot on this volume' <<<"$clog" || fail "the volume was taken for a fresh one"
! grep -q 'a home with a Nix profile on a fresh volume' <<<"$clog" || fail "the home's Nix profile was taken for a lost volume"
stamp=$(x cat /webtop-image-stamp)
[ "$(sudo cat "$dir/persist/nix/lower-stamp")" = "$stamp" ] || fail "lower-stamp is not the new image's stamp"
[ "$(sudo head -n 1 "$dir/persist/nix/lower-paths")" = "stamp $stamp" ] || fail "lower-paths does not start with the new stamp"
wait_markers
software_works "after the update"
stop
docker rm -f "$c" >/dev/null
echo "old volume under a new image: ok"
