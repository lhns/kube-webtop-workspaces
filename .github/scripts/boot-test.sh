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
sv=$(sed -n 's/.*system.stateVersion = "\(.*\)";.*/\1/p' "$(dirname "$0")/../../image/nixos/container.nix")
for f in flake.nix home.nix; do
  sed -e 's|@user@|user|g' -e 's|@home@|/home/user|g' -e "s|@stateVersion@|$sv|g" -e "s|@system@|$nixsys|g" \
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

# Software installation (ADR 0013): one declaration that `nix profile` edits. First the setup unit
# (after the template unit) switches to the new starter flake; the checks below follow.
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
decl=/home/ci/.config/home-manager/packages.json
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
! docker exec desktop-boot sh -c 'command -v discover' || fail "Discover is installed"
docker exec desktop-boot sh -c 'nix-store -qR /run/current-system | grep -q -- -knewstuff-' || fail "KNewStuff is not in the system"
[ "$(as_user sh -c 'command -v nix')" = /run/current-system/sw/bin/nix ] || fail "nix on the user's PATH is not the system's"
backend=$(as_user nix config show profile-backend)
[ "$backend" = "declaration:$decl" ] || fail "profile-backend is '$backend'"
as_user nix profile add nixpkgs#hello || fail "nix profile add nixpkgs#hello failed"
as_user jq -e '.elements.hello.originalUrl == "flake:nixpkgs"' "$decl" >/dev/null || fail "hello is not in packages.json"
as_user hello >/dev/null || fail "hello is not on PATH after the switch"
out=$(as_user nix profile list 2>&1)
{ grep -q 'hello' <<<"$out" && grep -q 'Backend: *declaration' <<<"$out"; } || fail "nix profile list: $out"
! grep -qE 'ignoring (the client-specified|untrusted)' <<<"$out" || fail "the daemon got the client-side settings: $out"
as_user nix profile list --json | jq -e '.elements.hello.backend == "declaration"
  and (.elements | to_entries | any(.value.backend == "profile" and (.value.storePaths[0] // "" | endswith("home-manager-path"))))' >/dev/null \
  || fail "nix profile list --json is not marked or lacks home-manager-path"
as_user home-manager switch --flake /home/ci/.config/home-manager >/dev/null || fail "a direct home-manager switch failed"
as_user nix profile remove hello || fail "nix profile remove hello failed"
! as_user jq -e '.elements.hello' "$decl" >/dev/null || fail "hello is still in packages.json"
! as_user sh -c 'command -v hello' >/dev/null || fail "hello is still installed"
# Not piped into grep -q: with pipefail, nix dying of SIGPIPE fails the pipeline.
out=$(as_user nix profile history)
grep -q 'hello' <<<"$out" || fail "nix profile history does not show hello: $out"
as_user nix profile rollback || fail "nix profile rollback failed"
as_user hello >/dev/null || fail "rollback did not bring hello back"
sum=$(as_user sha256sum "$decl")
! as_user nix profile remove --profile-hook /run/current-system/sw/bin/false hello 2>/dev/null || fail "a failing hook did not fail"
[ "$(as_user sha256sum "$decl")" = "$sum" ] || fail "a failing hook did not restore packages.json"
out=$(as_user nix profile remove --profile-hook '' hello 2>&1 || true)
grep -q "needs 'profile-hook'" <<<"$out" || fail "an empty hook was accepted: $out"
# Store paths and explicit profiles bypass the declaration.
jqpath=$(as_user sh -c 'dirname "$(dirname "$(readlink -f /run/current-system/sw/bin/jq)")"')
as_user nix profile add "$jqpath" || fail "adding a store path failed"
as_user nix profile list --json | jq -e '.elements.jq.backend == "profile"' >/dev/null || fail "the store path is not in the profile"
as_user nix profile remove jq || fail "removing the store path failed"
{ as_user nix profile add --profile-backend profile "$jqpath" && as_user nix profile remove --profile-backend profile jq; } \
  || fail "--profile-backend profile failed"
! as_user nix profile add --profile-backend profile --profile-hook /run/current-system/sw/bin/false "$jqpath" 2>/dev/null \
  || fail "a failing hook did not fail a profile change"
! as_user nix profile list --profile-backend profile --json | jq -e '.elements.jq' >/dev/null \
  || fail "a failing hook did not roll the profile back"
{ as_user nix profile add --profile /tmp/ci-profile nixpkgs#hello && as_user test -x /tmp/ci-profile/bin/hello; } \
  || fail "--profile <path> failed"
[ "$(as_user sha256sum "$decl")" = "$sum" ] || fail "a profile backend changed packages.json"
out=$(as_user nix profile list --profile /tmp/ci-profile --profile-backend profile 2>&1 || true)
grep -q 'cannot be used together' <<<"$out" || fail "--profile with --profile-backend was accepted: $out"
# A profile element of the same name moves into the declaration.
as_user nix profile add --profile-backend profile "$jqpath" || fail "adding jq to the profile failed"
as_user nix profile add nixpkgs#jq || fail "adding nixpkgs#jq failed"
as_user jq -e '.elements.jq' "$decl" >/dev/null || fail "jq is not declared"
! as_user nix profile list --profile-backend profile --json | jq -e '.elements.jq' >/dev/null || fail "jq is still in the profile"
as_user nix profile remove jq >/dev/null || fail "removing the declared jq failed"
# --all matches the declaration only: upgrade does not look at home-manager-path.
out=$(as_user nix profile upgrade --all 2>&1) || fail "nix profile upgrade --all failed: $out"
! grep -q 'not added from a flake' <<<"$out" || fail "upgrade --all reached the profile: $out"
# A store path may not shadow a declared package.
out=$(as_user nix profile add "$(as_user sh -c 'dirname "$(dirname "$(readlink -f "$(command -v hello)")")"')" 2>&1 || true)
grep -q 'is declared in the declaration' <<<"$out" || fail "a store path shadowing a declared package was accepted: $out"
# One command, two backends: when the declaration's hook fails, the profile change is undone too.
as_user sh -c 'printf "#!/bin/sh\n[ \"\$NIX_PROFILE_BACKEND\" != declaration ]\n" > /tmp/ci-decl-fails && chmod +x /tmp/ci-decl-fails'
sum=$(as_user sha256sum "$decl")
plink=$(as_user readlink /home/ci/.local/state/nix/profiles/profile)
! as_user nix profile add --profile-hook /tmp/ci-decl-fails "$jqpath" nixpkgs#tree 2>/dev/null \
  || fail "a failing declaration hook did not fail the command"
[ "$(as_user sha256sum "$decl")" = "$sum" ] || fail "packages.json changed although the command failed"
! as_user nix profile list --profile-backend profile --json | jq -e '.elements.jq' >/dev/null \
  || fail "the profile part of a failed command was kept"
[ "$(as_user readlink /home/ci/.local/state/nix/profiles/profile)" = "$plink" ] || fail "the profile is not back at its generation"
as_user hello >/dev/null || fail "hello went missing in a failed command"
# Rollback right after a hand edit goes back to the last recorded version; the edit becomes a version.
as_user sh -c "jq '.elements.handedit = {originalUrl: \"flake:nixpkgs\", attrPath: \"legacyPackages.$nixsys.tree\"}' $decl > /tmp/ci-decl && cat /tmp/ci-decl > $decl"
as_user nix profile rollback || fail "rollback after a hand edit failed"
[ "$(as_user sha256sum "$decl")" = "$sum" ] || fail "rollback after a hand edit did not restore the recorded version"
hist=$(as_user nix profile history | sed 's/\x1b\[[0-9;]*m//g')
newest=$(grep -o '^Version [0-9]*' <<<"$hist" | tail -n1 | cut -d' ' -f2)
tail -n 3 <<<"$hist" | grep -q 'tree' || fail "the hand edit is not the newest version: $hist"
as_user nix profile rollback --to "$newest" || fail "rollback --to the hand edit failed"
as_user jq -e '.elements.handedit' "$decl" >/dev/null || fail "rollback --to did not restore the hand edit"
as_user nix profile remove handedit >/dev/null || fail "removing the hand edit failed"
# Packages from another flake (builtins.getFlake), including a quoted attribute name with a dot.
as_user sh -c 'mkdir -p /tmp/ci-flake && printf "%s\n" "{" "  inputs.nixpkgs.url = \"nixpkgs\";" \
  "  outputs = { nixpkgs, ... }: { packages.'"$nixsys"' = { ci-hello = nixpkgs.legacyPackages.'"$nixsys"'.hello;" \
  "    \"ci.dotted\" = nixpkgs.legacyPackages.'"$nixsys"'.hello; }; };" "}" > /tmp/ci-flake/flake.nix'
# Locked first: locking writes flake.lock into the directory, which changes its hash.
as_user nix flake lock path:/tmp/ci-flake || fail "locking the local flake failed"
as_user nix profile add path:/tmp/ci-flake#ci-hello 'path:/tmp/ci-flake#"ci.dotted"' \
  || fail "adding packages from a local flake failed"
as_user jq -e '[.elements[] | select(.originalUrl | startswith("path:"))] | length == 2' "$decl" >/dev/null \
  || fail "the local flake's packages are not declared: $(as_user cat "$decl")"
as_user nix profile remove --regex '.*ci-flake.*' >/dev/null || fail "removing the local flake's packages failed"
# A flake dir that is a git repo: packages.json is added with --intent-to-add; a git-ignored one is refused.
hmdir=/home/ci/.config/home-manager
as_user sh -c "cd $hmdir && git init -q && git add -A && git rm -q --cached packages.json"
as_user nix profile add nixpkgs#tree || fail "nix profile add in a git flake dir failed"
as_user git -C "$hmdir" ls-files | grep -qx packages.json || fail "packages.json was not added to git"
as_user sh -c "cd $hmdir && echo packages.json > .gitignore && git rm -q --cached packages.json"
sum=$(as_user sha256sum "$decl")
out=$(as_user nix profile remove tree 2>&1 || true)
grep -q 'git ignores' <<<"$out" || fail "a git-ignored packages.json was not refused: $out"
[ "$(as_user sha256sum "$decl")" = "$sum" ] || fail "a refused change was not reverted"
as_user rm -rf "$hmdir/.git" "$hmdir/.gitignore"
as_user nix profile remove tree >/dev/null || fail "removing tree failed"
# The software center's backend against the patched Nix, next to a declared element it must leave alone.
as_user nix profile add nixpkgs#tree || fail "adding tree failed"
nsc=$(as_user sh -c 'readlink -f "$(command -v nix-software-center)"')
smoke=$(dirname "$(dirname "$nsc")")/libexec/nix-software-center/nsc-backend-smoke
as_user "$smoke" nixpkgs#cowsay --require-progress || fail "software center backend smoke failed"
as_user nix profile list --json | jq -e '.elements.tree.backend == "declaration" and (.elements.cowsay == null)' >/dev/null || fail "the smoke disturbed the declaration: $(as_user nix profile list --json)"
as_user nix profile remove tree >/dev/null || fail "removing tree after the smoke failed"
# An earlier starter flake (before packages.nix, for the old name `user`): the template unit rewrites it
# for ci with packages.nix, and the setup unit moves the `nix profile` flake packages into packages.json.
as_user nix profile remove hello >/dev/null || fail "nix profile remove hello failed"
# The shipped legacy template, so x86_64-linux on every runner: earlier images were amd64 only.
sed 's|@user@|user|g' "$(dirname "$0")/../../image/home-manager/legacy/flake-2.nix" > "$dir/legacy-flake.nix"
# Not docker cp: /home is a bind mount inside the container, which docker cp does not see.
as_user sh -c "echo $(base64 -w0 "$dir/legacy-flake.nix") | base64 -d > /home/ci/.config/home-manager/flake.nix"
as_user rm /home/ci/.config/home-manager/packages.nix "$decl"
# An earlier starter flake had no nix.conf (any nix.conf makes the setup "customized").
as_user rm /home/ci/.config/nix/nix.conf
as_user nix profile add --profile-backend profile nixpkgs#hello || fail "adding hello to the profile failed"
since=$(docker exec desktop-boot date '+%Y-%m-%d %H:%M:%S')
docker exec desktop-boot systemctl restart webtop-home-manager-template
docker exec desktop-boot journalctl -u webtop-home-manager-template --since "$since" -o cat | grep -q 'rewrote the starter flake' \
  || fail "the template unit did not rewrite the earlier flake"
docker exec desktop-boot systemctl start webtop-home-manager
wait_setup
[ "$(docker exec desktop-boot systemctl show -P Result webtop-home-manager)" = success ] \
  || { docker exec desktop-boot journalctl -u webtop-home-manager --no-pager | tail -n 50; fail "the migration failed"; }
as_user jq -e '.elements.hello.originalUrl == "flake:nixpkgs"' "$decl" >/dev/null || fail "the migration did not declare hello"
as_user grep -q 'packages.nix' /home/ci/.config/home-manager/flake.nix || fail "the migration did not add packages.nix"
as_user grep -qF 'homeConfigurations."ci"' /home/ci/.config/home-manager/flake.nix || fail "the migration did not rename the flake"
as_user nix profile list --json | jq -e '.elements.hello.backend == "declaration" and (.elements["hello-1"] == null)' >/dev/null \
  || fail "hello is not (only) declared after the migration: $(as_user nix profile list --json)"
as_user hello >/dev/null || fail "hello is not installed after the migration"
# home-manager's own `nix profile` calls in that switch went through the profile backend quietly
# (the deprecated `install` alias warning is stock Nix's).
warns=$(docker exec desktop-boot journalctl -u webtop-home-manager --since "$since" --no-pager | grep 'warning:' \
  | grep -v "deprecated alias\|cache.invalid" || true)
[ -z "$warns" ] || fail "nix profile warned during the switch: $warns"
# A stop between the import's `nix profile remove` and the switch: the next run only switches.
wst=/home/ci/.local/state/webtop
sum=$(as_user sha256sum "$decl")
as_user sh -c "touch $wst/packages-json-import && printf 'removed\n%s\nhello\n' \"\$(readlink /home/ci/.local/state/nix/profiles/profile)\" > $wst/import-phase"
since=$(docker exec desktop-boot date '+%Y-%m-%d %H:%M:%S')
docker exec desktop-boot systemctl start webtop-home-manager
wait_setup
[ "$(docker exec desktop-boot systemctl show -P Result webtop-home-manager)" = success ] || fail "the resumed import failed"
docker exec desktop-boot journalctl -u webtop-home-manager --since "$since" -o cat | grep -q 'resuming the import' \
  || fail "the unit did not resume the import"
as_user sh -c "! test -e $wst/import-phase && ! test -e $wst/packages-json-import && ! test -e $wst/home-manager-switch" \
  || fail "the resumed import left its state behind"
[ "$(as_user sha256sum "$decl")" = "$sum" ] || fail "the resumed import changed packages.json"
as_user hello >/dev/null || fail "hello is gone after the resumed import"
# A failing switch fails the unit and says so.
as_user sh -c "cp $decl /tmp/decl.bak && jq '.elements.broken = {originalUrl: \"flake:nixpkgs\", attrPath: \"legacyPackages.$nixsys.noSuchPackageCi\"}' /tmp/decl.bak > $decl"
gen=/home/ci/.local/state/nix/profiles/home-manager
link=$(as_user readlink "$gen")
as_user ln -sfn /nix/store/00000000000000000000000000000000-gone "$gen"
docker exec desktop-boot systemctl start webtop-home-manager
wait_setup
[ "$(docker exec desktop-boot systemctl show -P Result webtop-home-manager)" = exit-code ] || fail "a failing switch did not fail the unit"
docker exec desktop-boot journalctl -u webtop-home-manager --no-pager | grep -q 'home-manager switch failed' \
  || fail "a failing switch was not reported"
as_user cp /tmp/decl.bak "$decl"
as_user ln -sfn "$link" "$gen"
# A setup with a nix.conf of its own gets the rename only, and only once.
as_user sh -c "echo $(base64 -w0 "$dir/legacy-flake.nix") | base64 -d > /home/ci/.config/home-manager/flake.nix"
as_user rm /home/ci/.config/home-manager/packages.nix
as_user sh -c 'rm -f /home/ci/.config/nix/nix.conf && echo "# own" > /home/ci/.config/nix/nix.conf'
for run in 1 2; do
  since=$(docker exec desktop-boot date '+%Y-%m-%d %H:%M:%S')
  docker exec desktop-boot systemctl restart webtop-home-manager-template
  out=$(docker exec desktop-boot journalctl -u webtop-home-manager-template --since "$since" -o cat)
  case $run in
    1) grep -q 'kept without packages.nix' <<<"$out" || fail "an own nix.conf did not keep the flake without packages.nix: $out" ;;
    2) ! grep -q 'rewrote' <<<"$out" || fail "the rename-only rewrite repeats: $out" ;;
  esac
  sleep 1 # --since has second resolution
done
as_user grep -qF 'homeConfigurations."ci"' /home/ci/.config/home-manager/flake.nix || fail "the rename-only rewrite did not rename"
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
echo "software installation: ok"
echo "boot test passed"
