#!/bin/sh
# Run inside the desktop pod (see README). Prints PASS/FAIL per check; changes nothing under /persist.
export PATH=/run/current-system/sw/bin:/bin:/sbin:$PATH
pass=0; fail=0
ok()  { echo "PASS  $1${2:+  ($2)}"; pass=$((pass+1)); }
bad() { echo "FAIL  $1${2:+  ($2)}"; fail=$((fail+1)); }

um=$(awk 'NR==1{print $1" "$2" "$3}' /proc/self/uid_map)
[ "$um" != "0 0 4294967295" ] && ok uid_map_non_identity "$um" || bad uid_map_non_identity "$um"

awk '$5=="/sys/fs/cgroup"{n=split($6,o,","); for(i=1;i<=n;i++) if(o[i]=="rw") f=1} END{exit !f}' /proc/self/mountinfo \
  && ok cgroup2_rw || bad cgroup2_rw "$(grep ' /sys/fs/cgroup ' /proc/self/mountinfo)"

aa=$(cat /proc/self/attr/apparmor/current 2>/dev/null || cat /proc/self/attr/current 2>/dev/null)
case "$aa" in unconfined*) ok apparmor_unconfined "$aa";; *) bad apparmor_unconfined "'$aa'";; esac

sys=$(timeout 120 systemctl is-system-running --wait 2>&1)
case "$sys" in running|degraded) ok systemd_is_system_running "$sys";; *) bad systemd_is_system_running "$sys";; esac

findmnt /persist >/dev/null && ok persist_mounted "$(findmnt -n -o SOURCE,FSTYPE /persist)" || bad persist_mounted
{ findmnt /nix/store && findmnt /nix/var/nix; } >/dev/null && ok nix_overlay_mounted || bad nix_overlay_mounted

# A Wayland (konsole) and an X11 (konsole on xcb) client must both get a window; kwin lists them.
# The session bus only talks to its own user, so the probe runs as "user" (uid 1000).
G=$(mktemp /tmp/check-gui.XXXX); chmod 755 "$G"
cat > "$G" <<'GUI'
PATH=/run/current-system/sw/bin:$PATH
uid=$(id -u)
export XDG_RUNTIME_DIR=/run/user/$uid DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus
D=$(mktemp -d /run/user/$uid/check.XXXX)
cat > "$D/list.js" <<'JS'
var ws = workspace.windowList();
for (var i = 0; i < ws.length; i++) { var w = ws[i]; if (w.normalWindow || w.dialog) print("CHECKWIN class=" + w.resourceClass + " caption=" + w.caption); }
JS
env -u DISPLAY WAYLAND_DISPLAY=wayland-0 QT_QPA_PLATFORM=wayland timeout 25 konsole --separate -p tabtitle=CHECK-WAYLAND -e sleep 20 >"$D/k.log" 2>&1 &
XA=$(ls /run/user/$uid/xauth_* 2>/dev/null | head -1)
env -u WAYLAND_DISPLAY DISPLAY=:0 XAUTHORITY="$XA" QT_QPA_PLATFORM=xcb timeout 25 konsole --separate -p tabtitle=CHECK-X11 -e sleep 20 >"$D/x.log" 2>&1 &
sleep 6
id=$(qdbus org.kde.KWin /Scripting org.kde.kwin.Scripting.loadScript "$D/list.js" checklist)
since=$(date '+%Y-%m-%d %H:%M:%S')
qdbus org.kde.KWin /Scripting/Script$id org.kde.kwin.Script.run >/dev/null
sleep 1
qdbus org.kde.KWin /Scripting org.kde.kwin.Scripting.unloadScript checklist >/dev/null
journalctl --user --since "$since" --no-pager -o cat 2>/dev/null | grep CHECKWIN
pkill -f 'CHECK-'; rm -rf "$D"
GUI
wins=$(runuser -u user -- sh "$G"); rm -f "$G"
echo "$wins" | sed 's/^/      /'
echo "$wins" | grep -q CHECK-WAYLAND && ok wayland_window || bad wayland_window
echo "$wins" | grep -q CHECK-X11 && ok x11_window || bad x11_window

echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
