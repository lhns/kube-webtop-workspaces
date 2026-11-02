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
sv=$(sed -n 's/.*system.stateVersion = "\(.*\)";.*/\1/p' "$(dirname "$0")/../../image/nixos/container.nix")
for f in flake.nix home.nix; do
  sed -e 's|@user@|user|g' -e 's|@home@|/home/user|g' -e "s|@stateVersion@|$sv|g" \
    "$(dirname "$0")/../../image/home-manager/$f" > "$hm/$f"
done
if [ "$mode" = gray ]; then echo "# edited" >> "$hm/home.nix"; fi
cp "$hm/flake.nix" "$hm/home.nix" "$dir/"
sudo chown -R 1000:100 "$dir/persist/home"

python3 "$(dirname "$0")/fake-controller.py" 8081 "$reports" &
cleanup() {
  kill $! 2>/dev/null || true
  # Unreadable otherwise once the step has failed.
  if [ -s "$reports" ]; then echo "reports received:"; sed 's/^/  /' "$reports"; fi
}
trap cleanup EXIT

docker rm -f desktop-boot >/dev/null 2>&1 || true
args=(--privileged --cgroupns=private --shm-size=1g --add-host=host.docker.internal:host-gateway -p 8080:8080 -p 8083:8081
  -v "$dir/persist:/persist" -v "$dir/token:/token:ro"
  -e WEBTOP_CONTROLLER_URL=http://host.docker.internal:8081 -e WEBTOP_TOKEN_PATH=/token
  -e WEBTOP_HTTP_PORT=8080 -e WEBTOP_USER=ci -e WEBTOP_DESKTOP=ci -e WEBTOP_NAMESPACE=ci -e "WEBTOP_DISPLAY_NAME=CI Tester"
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
subst=$(docker exec desktop-boot /run/current-system/sw/bin/nix config show substituters || true)
case $subst in *cache.nixos.org*http://cache.invalid/ci*) echo "substituters: $subst" ;;
  *) echo "::error::WEBTOP_NIX_EXTRA_SUBSTITUTERS not in nix.conf (substituters: $subst)"; exit 1 ;; esac
# The desktop user is the desktop's name with uid 1000 and the display name; its home is the
# volume's, mounted at each valid home path and nothing else.
# Read once: grep -q on a pipe would SIGPIPE docker logs, which fails the pipeline (pipefail).
clog=$(docker logs desktop-boot 2>&1)
! grep 'webtop-identity:' <<<"$clog" || { echo "::error::the rename at activation failed"; exit 1; }
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
grep -qF '"loginName":"ci","homePaths":["/home/ci","/home/user"]' "$reports" \
  || { echo "::error::the agent did not report the login name and home paths"; exit 1; }
# The starter flake written for `user` now names ci, and switches.
grep -qF 'homeConfigurations."ci"' "$hm/flake.nix" \
  && grep -qF 'home.username = "ci";' "$hm/home.nix" \
  && grep -qF 'home.homeDirectory = "/home/ci";' "$hm/home.nix" \
  || { echo "::error::the home-manager template does not name ci"; cat "$hm/home.nix"; exit 1; }
start_hm=$(date +%s)
docker exec desktop-boot timeout 900 runuser -l ci -c 'home-manager switch --flake ~/.config/home-manager' 2>&1 | tail -n 20 \
  || { echo "::error::home-manager switch failed"; exit 1; }
echo "home-manager switch took $(($(date +%s) - start_hm)) s"
echo "identity: $pw; /home: $homes"
echo "boot test passed"
