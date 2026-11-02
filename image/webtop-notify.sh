# usage: webtop-notify TITLE BODY [ICON]
# A desktop notification for the user's session, from root or from the user. Best effort: Plasma's
# notification server may not be up yet, so it retries, and blocks (callers run it in the
# background). $uid is set by the unit definition.
icon=${3:-dialog-information}
run=()
if [ "$(id -u)" = 0 ]; then run=(runuser -u "$(id -nu "$uid")" --); fi
for _ in $(seq 1 "${WEBTOP_NOTIFY_TRIES:-12}"); do
  # timeout: a call to an unowned name can hang until the D-Bus timeout.
  if timeout 5 "${run[@]}" env "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus" \
      notify-send -a Desktop -i "$icon" "$1" "$2" 2>/dev/null; then
    exit 0
  fi
  sleep 5
done
echo "notification not delivered: $1" >&2
