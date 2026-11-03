# usage: webtop-restore-progress start TOTAL | total TOTAL | current TEXT | step | finish [MESSAGE] | nixconfig
# The software restore's progress for the agent (agent/internal/restore): steps are the reinstalled
# profile entries plus the home-manager switch. Atomic, so the reader never sees a partial file.
# `total`, `current` and `step` do nothing unless a restore is running, so the setup script can call
# them on any boot. `nixconfig` prints a NIX_CONFIG that makes nix log its events to a file the agent
# follows for a real progress bar: NIX_CONFIG=$(webtop-restore-progress nixconfig) nix ... `finish` with a message marks the restore failed.
f=/run/webtop/restore/progress.json
log=/run/webtop/restore/nix.jsonl
cur() { cat "$f" 2>/dev/null || echo '{}'; }
commit() { mv -f "$f.tmp" "$f"; }
active() { [ "$(cur | jq '.active // false')" = true ]; }

case $1 in
  start) rm -f "$log"; jq -nc --argjson t "$2" '{active: true, done: 0, total: $t, current: "", failed: false, message: ""}' > "$f.tmp" && commit ;;
  total) if active; then cur | jq -c --argjson t "$2" '.total = $t' > "$f.tmp" && commit; fi ;;
  current) if active; then cur | jq -c --arg c "$2" '.current = $c' > "$f.tmp" && commit; fi ;;
  step) if active; then cur | jq -c '.done = ([.done + 1, .total] | min)' > "$f.tmp" && commit; fi ;;
  finish)
    cur | jq -c --arg m "${2:-}" '.active = false | .current = "" | .message = $m | .failed = ($m != "")
      | .done = (if $m == "" then .total else .done end)' > "$f.tmp" && commit ;;
  nixconfig)
    printf '%s' "${NIX_CONFIG:-}"
    # Root and the user both append to it; the file stays writable for whoever comes second.
    if active; then
      printf '
json-log-path = %s
' "$log"
      : >> "$log" && chmod 666 "$log" 2>/dev/null || true
    fi ;;
  *) echo "usage: webtop-restore-progress start TOTAL | total TOTAL | current TEXT | step | finish [MESSAGE] | nixconfig" >&2; exit 2 ;;
esac
