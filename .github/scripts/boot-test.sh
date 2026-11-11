#!/usr/bin/env bash
# Boots the desktop image under docker (systemd as PID 1) and waits until the in-pod agent reports
# Ready, which needs KWin to be compositing (a gray screen never gets there). A fake controller
# receives the agent's reports. Then probes KWin and the Selkies port directly.
#
# Usage: boot-test.sh <image> <timeout-seconds> [gray]
#   gray: a runtime drop-in sets KWIN_COMPOSE=Q (KWin 6.8 then does not composite). The test passes
#         only if the agent reports "Compositor not active" and never Ready: it proves the check can fail.
# Leaves the container "desktop-boot" for the caller to dump.
set -euo pipefail

image=$1
timeout=$2
mode=${3:-ready}
# The runner is native, so this is the image's system too.
nixsys=$(uname -m)-linux
dir=$(mktemp -d)
# Cluster-level Selkies settings (docs/contract.md): quote, backslash, $, =, backtick, non-ASCII.
title='Ci "q" $HOME `x` \ a=b ä – Desktop'
probe='\\"$X=1 #'
# webtop-init refuses a tab; a newline ends the value, so the injected second line is dropped.
tab=$(printf 'a\tb')
nl=$(printf 'x\nSELKIES_CI_INJECT=1')
reports=$dir/reports
touch "$reports"
echo token > "$dir/token"
mkdir "$dir/persist"
# A volume from before the login names, its home-paths already edited: /home/user is kept, the rest
# must be ignored (ADR 0011). Owned by uid 1000 like a real home.
hp=$dir/persist/home/user/.config/webtop
mkdir -p "$hp"
printf '/home/user\nroot\n../x\n/home/root\n/home/../x\n' > "$hp/home-paths"
# The starter home-manager flake as the image wrote it for `user`: rewritten for ci at boot. In the gray
# run it is edited, so it must stay as it is, with the lines to change in the journal.
hm=$dir/persist/home/user/.config/home-manager
mkdir -p "$hm"
# The system's own template (webtop-nix's nixProfile), system and stateVersion already filled in.
for f in flake.nix home.nix; do
  src=$(nix eval --raw ".#nixosConfigurations.desktop-$nixsys.config.environment.etc.\"webtop/home-manager/$f\".source")
  sed -e 's|@user@|user|g' -e 's|@home@|/home/user|g' "$src" > "$hm/$f"
done
if [ "$mode" = gray ]; then echo "# edited" >> "$hm/home.nix"; fi
cp "$hm/flake.nix" "$hm/home.nix" "$dir/"
sudo chown -R 1000:100 "$dir/persist/home"

python3 "$(dirname "$0")/fake-controller.py" 8081 "$reports" &
controller=$!
cleanup() {
  kill $controller 2>/dev/null || true
  # Unreadable otherwise once the step has failed.
  if [ -s "$reports" ]; then echo "reports received:"; sed 's/^/  /' "$reports"; fi
}
trap cleanup EXIT

docker rm -f desktop-boot >/dev/null 2>&1 || true
args=(--privileged --cgroupns=private --shm-size=1g --add-host=host.docker.internal:host-gateway -p 8080:8080 -p 8083:8081
  -v "$dir/persist:/persist" -v "$dir/token:/token:ro"
  -e WEBTOP_CONTROLLER_URL=http://host.docker.internal:8081 -e WEBTOP_TOKEN_PATH=/token
  -e WEBTOP_HTTP_PORT=8080 -e WEBTOP_MAX_APP_SCREENS=2 -e NSC_UNFREE_LISTED_DEFAULT=0 -e WEBTOP_USER=ci -e WEBTOP_DESKTOP=ci -e WEBTOP_NAMESPACE=ci -e "WEBTOP_DISPLAY_NAME=CI Tester"
  -e WEBTOP_TIME_ZONE=Asia/Tokyo -e WEBTOP_LOCALE=de_DE.UTF-8
  -e WEBTOP_NIX_EXTRA_SUBSTITUTERS=http://cache.invalid/ci -e "WEBTOP_NIX_EXTRA_TRUSTED_PUBLIC_KEYS=ci:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
  -e "WEBTOP_SELKIES_UI_TITLE=$title" -e "WEBTOP_SELKIES_CI_PROBE=$probe"
  -e "WEBTOP_SELKIES_CI_TAB=$tab" -e "WEBTOP_SELKIES_CI_NL=$nl")
if [ "$mode" = gray ]; then
  mkdir -p "$dir/dropin"
  printf '[Service]\nEnvironment=KWIN_COMPOSE=Q\n' > "$dir/dropin/gray.conf"
  # /run must be a tmpfs already, so that the drop-in is not hidden by webtop-init's own mount.
  args+=(--tmpfs "/run:rw,nosuid,nodev,exec,mode=755" -v "$dir/dropin:/run/systemd/system/webtop-session.service.d:ro"
    -e WEBTOP_COMPOSITOR_TIMEOUT=45s)
fi
# --privileged stands in for the pod's SYS_ADMIN, unmasked /proc and unconfined AppArmor.
# /persist is a bind mount: the store overlay's upperdir cannot sit on the container's own overlayfs.
start=$(date +%s)
docker run -d --name desktop-boot "${args[@]}" "$image" >/dev/null

in_session() { docker exec desktop-boot runuser -u ci -- env PATH=/run/current-system/sw/bin \
  XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus "$@"; }

want='"ready":true'
[ "$mode" != gray ] || want='Compositor not active'
deadline=$((start + timeout))
last=
while :; do
  if [ "$(docker inspect -f '{{.State.Running}}' desktop-boot)" != true ]; then
    echo "::error::the container exited during boot"; exit 1
  fi
  if [ "$mode" = gray ] && grep -q '"ready":true' "$reports"; then
    echo "::error::the agent reported Ready with compositing disabled"; exit 1
  fi
  if grep -q "$want" "$reports"; then
    echo "found '$want' after $(($(date +%s) - start)) s"; break
  fi
  stage=$(tail -n1 "$reports" | sed 's/^[^ ]* [^ ]* //')
  [ "$stage" = "$last" ] || { echo "[$(($(date +%s) - start)) s] $stage"; last=$stage; }
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "::error::no '$want' within ${timeout}s (last: ${last:-nothing})"; exit 1
  fi
  sleep 2
done

echo "container pid 1 age: $(docker exec desktop-boot ps -o etimes= -p 1) s; systemd: $(docker exec desktop-boot systemctl is-system-running || true)"
in_session sh -c 'ps -u ci -o etimes=,args= | grep -E "[k]win_wayland|[s]elkies"' | cut -c1-200 || true
comp=$(in_session busctl --user get-property org.kde.KWin /Compositor org.kde.kwin.Compositing active || true)
echo "Compositing active: ${comp:-<no answer>}"

if [ "$mode" = gray ]; then
  [ "$comp" != "b true" ] || { echo "::error::KWin composites although KWIN_COMPOSE=Q"; exit 1; }
  # The edited starter flake is left alone; the journal and a notification name the lines to change.
  cmp -s "$hm/flake.nix" "$dir/flake.nix" && cmp -s "$hm/home.nix" "$dir/home.nix" \
    || { echo "::error::the edited home-manager flake was changed"; exit 1; }
  hmlog=$(docker exec desktop-boot journalctl -u webtop-home-manager-template -o cat)
  grep -qF 'home.username = "user";  ->  home.username = "ci";' <<<"$hmlog" \
    || { echo "::error::no home-manager notice in the journal"; echo "$hmlog"; exit 1; }
  grep -q '^notifying:' <<<"$(docker exec desktop-boot journalctl -u webtop-home-manager-notice -o cat)" \
    || { echo "::error::webtop-home-manager-notice did not run"; exit 1; }
  echo "gray-screen case failed as intended"
  exit 0
fi

[ "$comp" = "b true" ] || { echo "::error::KWin is not compositing"; exit 1; }
in_session systemctl --user is-active --quiet plasma-workspace.target \
  || { echo "::error::plasma-workspace.target is not active"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/ || true)
case $code in 2??|3??|401) echo "Selkies answers HTTP $code" ;; *) echo "::error::Selkies port answers HTTP $code"; exit 1 ;; esac
# One app per screen: the helper is installed and the agent serves its API (404: not an application).
in_session sh -c 'command -v webtop-app-screen && command -v kioclient' >/dev/null \
  || { echo "::error::webtop-app-screen or kioclient is not on the user's PATH"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8083/api/v1/apps/no-such-app.invalid || true)
[ "$code" = 404 ] || { echo "::error::the agent's apps API answers HTTP $code, want 404"; exit 1; }
# Each app gets its own screen (cap 2 here). The agent itself must find the helper: the user's PATH
# above is not the one it looks in. Nothing has connected a page, so the open apps count as reserved.
in_session webtop-app-screen screens >/dev/null || { echo "::error::webtop-app-screen screens failed"; exit 1; }
api=http://localhost:8083/api/v1
app_display() { curl -fsS -X POST "$api/apps/$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["display"])'; }
read -r app2 app3 < <(curl -fsS "$api/apps" | python3 -c 'import json,sys; print(*[a["id"] for a in json.load(sys.stdin) if a["id"] != "org.kde.dolphin"][:2])')
[ -n "${app3:-}" ] || { echo "::error::the image has fewer than three applications"; exit 1; }
d1=$(app_display org.kde.dolphin) || { echo "::error::the agent cannot open org.kde.dolphin"; exit 1; }
d2=$(app_display "$app2") || { echo "::error::the agent cannot open $app2"; exit 1; }
[ "$d1" -ge 3 ] && [ "$d2" -ge 3 ] && [ "$d1" != "$d2" ] || { echo "::error::app displays '$d1' '$d2', want distinct numbers >= 3"; exit 1; }
[ "$(app_display org.kde.dolphin)" = "$d1" ] || { echo "::error::a second open of dolphin got another display"; exit 1; }
code=$(curl -s -o "$dir/cap.json" -w '%{http_code}' -X POST "$api/apps/$app3" || true)
[ "$code" = 409 ] || { echo "::error::a third app answers HTTP $code, want 409"; exit 1; }
python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); assert c["max"]==2 and sorted(c["open"])==sorted(sys.argv[2:]), c' "$dir/cap.json" org.kde.dolphin "$app2"   || { echo "::error::the 409 does not name the open apps:"; cat "$dir/cap.json"; exit 1; }
! in_session systemctl --user list-units --no-legend 'app-*' | grep -qF "$app3" || { echo "::error::$app3 started although over the cap"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$api/focus/main" || true)
[ "$code" = 204 ] || { echo "::error::focus main answers HTTP $code, want 204"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$api/focus/9999" || true)
[ "$code" = 404 ] || { echo "::error::focus of an unknown screen answers HTTP $code, want 404"; exit 1; }
# The overview's source: the application list is a non-empty JSON array of entries with an id.
curl -fsS http://localhost:8083/api/v1/apps | python3 -c 'import json,sys; a=json.load(sys.stdin); assert a and all(x["id"] and x["name"] for x in a), a' \
  || { echo "::error::the agent's app list is not a non-empty JSON array of apps"; exit 1; }
# webtop-init -> /run/webtop/selkies.env -> the Selkies process: values arrive exactly, refused ones not at all.
pid=$(in_session systemctl --user show -p MainPID --value selkies)
sel=$(docker exec desktop-boot cat "/proc/$pid/environ" | tr '\0' '\n' | grep -a '^SELKIES_' || true)
for want in "SELKIES_UI_TITLE=$title" "SELKIES_CI_PROBE=$probe" "SELKIES_PORT=8080"; do
  grep -qaFx -- "$want" <<<"$sel" || { echo "::error::Selkies' environment lacks '$want':"; echo "$sel"; exit 1; }
done
if grep -qE 'SELKIES_CI_(TAB|INJECT)' <<<"$sel"; then
  echo "::error::a refused Selkies setting reached the process:"; echo "$sel"; exit 1
fi
echo "Selkies environment carries the cluster settings exactly"
# webtop-init -> /run/webtop/session.env -> the Plasma session (and so the apps it starts).
spid=$(docker exec desktop-boot systemctl show -P MainPID webtop-session)
senv=$(docker exec desktop-boot cat "/proc/$spid/environ" | tr '\0' '\n')
grep -qaFx 'NSC_UNFREE_LISTED_DEFAULT=0' <<<"$senv" || { echo "::error::the session lacks NSC_UNFREE_LISTED_DEFAULT=0"; exit 1; }
subst=$(docker exec desktop-boot /run/current-system/sw/bin/nix config show substituters || true)
case $subst in *cache.nixos.org*http://cache.invalid/ci*) echo "substituters: $subst" ;;
  *) echo "::error::WEBTOP_NIX_EXTRA_SUBSTITUTERS not in nix.conf (substituters: $subst)"; exit 1 ;; esac
# The desktop user is the desktop's name with uid 1000 and the display name; its home is the
# volume's, mounted at each valid home path and nothing else.
# Read once: grep -q on a pipe would SIGPIPE docker logs, which fails the pipeline (pipefail).
clog=$(docker logs desktop-boot 2>&1)
! grep 'webtop-identity:' <<<"$clog" || { echo "::error::the rename at activation failed"; exit 1; }
! docker exec desktop-boot test -e /run/webtop/identity-error || { echo "::error::identity-error written"; exit 1; }
pw=$(docker exec desktop-boot getent passwd ci || true)
case $pw in "ci:x:1000:100:CI Tester:/home/ci:"?*) ;; *) echo "::error::passwd entry of ci: '$pw'"; exit 1 ;; esac
[ "$(docker exec desktop-boot loginctl show-user 1000 --property=Name --value)" = ci ] \
  || { echo "::error::the logind user 1000 is not ci"; exit 1; }
# The session leader's environment (startplasma-wayland, started by systemd with User=1000).
senv=$(docker exec desktop-boot sh -c 'tr "\0" "\n" < /proc/$(systemctl show -P MainPID webtop-session)/environ' || true)
grep -qx 'HOME=/home/ci' <<<"$senv" \
  || { echo "::error::the session's HOME is not /home/ci"; echo "$senv" | grep -E '^(HOME|USER)='; exit 1; }
docker exec desktop-boot sh -c 'grep -q "^ci:" /etc/subuid && grep -q "^ci:" /etc/subgid && ! grep -q "^user:" /etc/subuid /etc/subgid' \
  || { echo "::error::subordinate id ranges not renamed to ci"; docker exec desktop-boot sh -c 'ls -l /etc/subuid /etc/subgid; cat /etc/subuid /etc/subgid'; exit 1; }
docker exec desktop-boot test -e /var/lib/systemd/linger/ci || { echo "::error::ci does not linger"; exit 1; }
groups=" $(docker exec desktop-boot id -nG ci) "
case $groups in *" wheel "*) ;; *) echo "::error::groups of ci:$groups"; exit 1 ;; esac
case $groups in *" webtop "*) ;; *) echo "::error::groups of ci:$groups"; exit 1 ;; esac
homes=$(docker exec desktop-boot ls /home | tr '\n' ' ')
[ "$homes" = "ci user " ] || { echo "::error::/home holds: $homes"; exit 1; }
vol=$(docker exec desktop-boot stat -c %d:%i /persist/home/user)
for h in /home/ci /home/user; do
  docker exec desktop-boot mountpoint -q "$h" && [ "$(docker exec desktop-boot stat -c %d:%i "$h")" = "$vol" ] \
    || { echo "::error::$h is not a mount of the volume's home"; exit 1; }
done
grep -qx /home/ci "$hp/home-paths" || { echo "::error::/home/ci not added to home-paths"; cat "$hp/home-paths"; exit 1; }
for bad in root ../x /home/root /home/../x; do
  grep -qF "home-paths: ignoring '$bad'" <<<"$clog" || { echo "::error::'$bad' not reported as ignored"; exit 1; }
done
grep -qF '"loginName":"ci","homePaths":["/home/ci","/home/user"],"loginError":""' "$reports" \
  || { echo "::error::the agent did not report the login name and home paths"; exit 1; }

# The setup unit (after the template unit) switches to the new starter flake. What `nix profile` and the
# template do is tested in webtop-nix (tests/nix-profile-image-test.sh).
fail() { echo "::error::$*"; exit 1; }
as_user() { in_session env HOME=/home/ci PATH=/home/ci/.nix-profile/bin:/home/ci/.local/state/nix/profiles/profile/bin:/run/current-system/sw/bin "$@"; }
unit_prop() { docker exec desktop-boot systemctl show -P "$1" webtop-home-manager; }
# Until the setup unit's run ends; a unit that has not run yet also has Result=success.
wait_setup() {
  for _ in $(seq 1 150); do
    [ "$(unit_prop ActiveState)" = active ] || [ "$(unit_prop ExecMainExitTimestampMonotonic)" = 0 ] || return 0
    sleep 2
  done
}
wait_setup
[ "$(docker exec desktop-boot systemctl show -P Result webtop-home-manager)" = success ] \
  || { docker exec desktop-boot journalctl -u webtop-home-manager --no-pager | tail -n 50; fail "webtop-home-manager failed"; }
# The starter flake written for `user` now names ci, and switches.
grep -qF 'homeConfigurations."ci"' "$hm/flake.nix" \
  && grep -qF 'home.username = "ci";' "$hm/home.nix" \
  && grep -qF 'home.homeDirectory = "/home/ci";' "$hm/home.nix" \
  || { echo "::error::the home-manager template does not name ci"; cat "$hm/home.nix"; exit 1; }
as_user test -e /home/ci/.local/state/nix/profiles/home-manager || fail "the setup unit did not switch"
echo "home-manager switch took $((($(unit_prop ExecMainExitTimestampMonotonic) - $(unit_prop ExecMainStartTimestampMonotonic)) / 1000000)) s"
echo "identity: $pw; /home: $homes"
# The restore scripts' progress file reaches the controller in the agent's report, and the
# tray job tracker runs.
docker exec desktop-boot systemctl is-active webtop-restore-job >/dev/null || fail "webtop-restore-job is not running"
as_user webtop-restore-progress start 2 || fail "webtop-restore-progress start failed"
as_user webtop-restore-progress current "Reinstalling ci" && as_user webtop-restore-progress step
for _ in $(seq 1 15); do
  grep -qF '"softwareRestore":{"active":true,"done":1,"total":2,"failed":false}' "$reports" && break
  sleep 1
done
grep -qF '"softwareRestore":{"active":true,"done":1,"total":2,"failed":false}' "$reports" \
  || fail "the agent did not report the restore progress: $(tail -n 3 "$reports")"
as_user webtop-restore-progress finish
for _ in $(seq 1 15); do
  grep -qF '"softwareRestore":{"active":false,"done":2,"total":2,"failed":false}' "$reports" && break
  sleep 1
done
grep -qF '"softwareRestore":{"active":false,"done":2,"total":2,"failed":false}' "$reports" \
  || fail "the agent did not report the finished restore"
echo "restore progress report: ok"
# Time zone and locale (docs/contract.md): the chart env reaches the system and the session. The user's
# session.conf and its bad values are tested in webtop-nix (tests/vm.nix).
tzl_check() { # <zone> <locale> <abbreviation>
  local env
  [ "$(docker exec desktop-boot readlink /etc/localtime)" = "/etc/zoneinfo/$1" ] || fail "/etc/localtime is not $1: $(docker exec desktop-boot ls -l /etc/localtime)"
  [ "$(docker exec desktop-boot timedatectl show -P Timezone)" = "$1" ] || fail "timedatectl does not report $1"
  docker exec desktop-boot localectl status | grep -qF "LANG=$2" || fail "localectl does not report $2"
  # Past the wrapper (which loads /etc/locale.conf), then startplasma's own environment.
  for _ in $(seq 1 60); do
    docker exec desktop-boot sh -c 'grep -aq startplasma /proc/$(systemctl show -P MainPID webtop-session)/cmdline' && break
    sleep 1
  done
  env=$(docker exec desktop-boot sh -c 'tr "\0" "\n" < /proc/$(systemctl show -P MainPID webtop-session)/environ')
  grep -qx "LANG=$2" <<<"$env" || fail "the session's LANG is not $2: $(grep -E '^(LANG|LC_)' <<<"$env")"
  ! grep -qE '^(TZ|LC_ALL)=' <<<"$env" || fail "TZ or LC_ALL in the session's environment"
  [ "$(docker exec desktop-boot sh -c 'env -u TZ date +%Z')" = "$3" ] || fail "date does not show $3"
  [ "$(docker exec desktop-boot systemctl show -P ActiveState webtop-session)" = active ] || fail "the session is not active"
}
tzl_check Asia/Tokyo de_DE.UTF-8 JST
echo "time zone and locale: ok"

# Sleep (webtop-nix's kww wiring): the freeze is reported to the controller, a Selkies client wakes
# it, and a stop with the "suspend" reason is reported as such. The unit is started directly, as in
# webtop-nix's tests/power.nix: logind's own checks for suspend are the distro's.
last_report() { grep -o '"asleep":[a-z]*' "$reports" | tail -n 1; }
wait_asleep() { # <true|false>
  for _ in $(seq 1 60); do
    [ "$(last_report)" = "\"asleep\":$1" ] && return 0
    sleep 1
  done
  fail "the controller was not told asleep=$1 (last: $(last_report)): $(tail -n 3 "$reports")"
}
docker exec desktop-boot systemctl start --no-block systemd-suspend.service
wait_asleep true
docker exec desktop-boot test -s /run/webtop/frozen-units || fail "asleep was reported but no unit is frozen"
python3 "$(dirname "$0")/ws-client.py" 8080 &
wsclient=$!
trap 'kill $wsclient 2>/dev/null || true; cleanup' EXIT
wait_asleep false
! docker exec desktop-boot test -s /run/webtop/frozen-units || fail "awake was reported but units are still frozen"
kill $wsclient
echo "sleep and wake: ok"
# A stop with the reason marker (what Sleep in poweroff mode leaves) reaches the controller as suspend.
docker exec desktop-boot sh -c 'echo suspend > /run/webtop/stop-reason && systemctl poweroff' || true
for _ in $(seq 1 120); do
  grep -qF '{"action":"suspend","reason":"suspend"}' "$reports" && break
  sleep 1
done
grep -qF '{"action":"suspend","reason":"suspend"}' "$reports" || fail "no power report with reason suspend: $(tail -n 3 "$reports")"
echo "suspend stop reason: ok"
echo "boot test passed"
