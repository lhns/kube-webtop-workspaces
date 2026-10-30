#!/usr/bin/env bash
# Boots the desktop image under docker (systemd as PID 1) and waits until the in-pod agent reports
# Ready, which needs KWin to be compositing (a gray screen never gets there). A fake controller
# receives the agent's reports. Then probes KWin and the Selkies port directly.
# Usage: boot-test.sh <image> [timeout-seconds]. Leaves the container "desktop-boot" for the caller to dump.
set -euo pipefail

image=$1
timeout=${2:-900}
dir=$(mktemp -d)
reports=$dir/reports
touch "$reports"
echo token > "$dir/token"

python3 "$(dirname "$0")/fake-controller.py" 8081 "$reports" &
trap 'kill $! 2>/dev/null || true' EXIT

# --privileged stands in for the pod's SYS_ADMIN, unmasked /proc and unconfined AppArmor.
# /persist is a bind mount: the store overlay's upperdir cannot sit on the container's own overlayfs.
mkdir "$dir/persist"
docker run -d --name desktop-boot --privileged --cgroupns=private --shm-size=1g \
  --add-host=host.docker.internal:host-gateway -p 8080:8080 \
  -v "$dir/persist:/persist" -v "$dir/token:/token:ro" \
  -e WEBTOP_CONTROLLER_URL=http://host.docker.internal:8081 -e WEBTOP_TOKEN_PATH=/token \
  -e WEBTOP_HTTP_PORT=8080 -e WEBTOP_USER=ci -e WEBTOP_DESKTOP=ci -e WEBTOP_NAMESPACE=ci \
  "$image" >/dev/null

in_session() { docker exec desktop-boot runuser -u user -- env PATH=/run/current-system/sw/bin \
  XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus "$@"; }

deadline=$((SECONDS + timeout))
last=
while :; do
  if [ "$(docker inspect -f '{{.State.Running}}' desktop-boot)" != true ]; then
    echo "::error::the container exited during boot"; exit 1
  fi
  if grep -q '"ready":true' "$reports"; then
    echo "agent reported Ready after $SECONDS s"; break
  fi
  stage=$(tail -n1 "$reports" | sed 's/^[^ ]* //')
  [ "$stage" = "$last" ] || { echo "[$SECONDS s] $stage"; last=$stage; }
  if [ $SECONDS -ge $deadline ]; then
    echo "::error::the agent did not report Ready within ${timeout}s (last: ${last:-nothing})"; exit 1
  fi
  sleep 5
done

# The agent's own probes, once more and independently.
in_session busctl --user get-property org.kde.KWin /Compositor org.kde.kwin.Compositing active | tee /dev/stderr | grep -qx 'b true' \
  || { echo "::error::KWin is not compositing"; exit 1; }
in_session systemctl --user is-active --quiet plasma-workspace.target \
  || { echo "::error::plasma-workspace.target is not active"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/ || true)
case $code in 2??|3??|401) echo "Selkies answers HTTP $code" ;; *) echo "::error::Selkies port answers HTTP $code"; exit 1 ;; esac
echo "boot test passed"
