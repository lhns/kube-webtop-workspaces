# Helpers of lower-swap-test.sh and old-volume-test.sh, sourced after they set image, timeout, dir
# (a scratch volume at $dir/persist) and c (the container's name).
fail() { echo "::error::$*"; exit 1; }
x() { docker exec "$c" "$@"; }

boot() { # [image]
  docker rm -f "$c" >/dev/null 2>&1 || true
  docker run -d --name "$c" --privileged --cgroupns=private --shm-size=1g -v "$dir/persist:/persist" \
    -e WEBTOP_DESKTOP=ci -e WEBTOP_USER=ci -e WEBTOP_CONTROLLER_URL=http://127.0.0.1:9 "${1:-$image}" >/dev/null
  local deadline=$(($(date +%s) + timeout))
  until x nix-store -q --hash /run/current-system >/dev/null 2>&1; do
    [ "$(docker inspect -f '{{.State.Running}}' "$c")" = true ] || { docker logs "$c" 2>&1 | tail -n 50; fail "the container exited"; }
    [ "$(date +%s)" -lt "$deadline" ] || { docker logs "$c" 2>&1 | tail -n 50; fail "no nix-daemon within ${timeout}s"; }
    sleep 2
  done
}
# Not waiting for the power hook's report to the (absent) controller; webtop-init must cope anyway.
stop() { docker stop -t 15 "$c" >/dev/null; }
as_user() { x runuser -u ci -- env HOME=/home/ci PATH=/home/ci/.nix-profile/bin:/home/ci/.local/state/nix/profiles/profile/bin:/run/current-system/sw/bin "$@"; }
journal() { x journalctl -u "$1" --no-pager -o cat | tail -n 400; }
wait_setup() {
  for _ in $(seq 1 $((timeout / 3))); do
    [ "$(x systemctl show -P ActiveState webtop-home-manager)" = active ] || [ "$(x systemctl show -P ExecMainExitTimestampMonotonic webtop-home-manager)" = 0 ] || return 0
    sleep 3
  done
  journal webtop-home-manager; fail "the setup unit did not finish"
}
# Until the image update's work is done: the markers go only after it succeeded.
wait_markers() {
  for _ in $(seq 1 $((timeout / 3))); do
    sudo test -e "$dir/persist/nix/reinstall/restore" || sudo test -e "$dir/persist/nix/reinstall/pending" || return 0
    sleep 3
  done
  journal webtop-nix-restore; journal webtop-nix-reinstall; fail "the update's markers are still there"
}
software_works() { # <what>
  as_user hello >/dev/null || { journal webtop-nix-reinstall; fail "$1: the declared package does not run"; }
  as_user cowsay ok >/dev/null || { journal webtop-nix-reinstall; fail "$1: the undeclared profile package does not run"; }
  as_user nix profile list --json | jq -e '.elements.hello.backend == "declaration" and .elements.cowsay.backend == "profile"' >/dev/null \
    || fail "$1: nix profile list: $(as_user nix profile list)"
  as_user test -e /home/ci/.local/state/nix/profiles/home-manager || fail "$1: no home-manager generation"
  x nix-store --verify --check-contents || fail "$1: the store does not verify"
}

