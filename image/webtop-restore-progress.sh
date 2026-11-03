# usage: webtop-restore-progress start TOTAL | total TOTAL | current TEXT | done | finish [MESSAGE]
# The software restore's progress for the agent (agent/internal/restore): steps are the reinstalled
# profile entries plus the home-manager switch. Atomic, so the reader never sees a partial file.
# `current` and `done` do nothing unless a restore is running, so the setup script can call them
# on any boot. `finish` with a message marks the restore failed.
# shellcheck disable=SC2016 # jq programs
f=/run/webtop/restore/progress.json
cur() { cat "$f" 2>/dev/null || echo '{}'; }
put() { jq -c "$@" > "$f.tmp" && mv -f "$f.tmp" "$f"; }
active() { [ "$(cur | jq '.active // false')" = true ]; }

case $1 in
  start) jq -nc --argjson t "$2" '{active: true, done: 0, total: $t, current: "", failed: false, message: ""}' > "$f.tmp" && mv -f "$f.tmp" "$f" ;;
  total) if active; then cur | put --argjson t "$2" '.total = $t'; fi ;;
  current) if active; then cur | put --arg c "$2" '.current = $c'; fi ;;
  done) if active; then cur | put '.done = ([.done + 1, .total] | min)'; fi ;;
  finish)
    msg=${2:-}
    cur | put --arg m "$msg" '.active = false | .current = "" | .message = $m | .failed = ($m != "")
      | .done = (if $m == "" then .total else .done end)' ;;
  *) echo "usage: webtop-restore-progress start TOTAL | total TOTAL | current TEXT | done | finish [MESSAGE]" >&2; exit 2 ;;
esac
