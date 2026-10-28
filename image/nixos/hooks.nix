# The in-pod agent (agent/) and the units that tie the desktop's power actions, boot progress and
# activity to the controller's power/report API (docs/contract.md, section 8).
{ config, lib, pkgs, agent, ... }:

let
  cfg = config.services.webtop;
  uid = toString cfg.uid;
  exe = "${agent}/bin/webtop-agent";
  envFile = "-/run/webtop/env";
  userBus = "unix:path=/run/user/${uid}/bus";

  reinstall = pkgs.writeShellApplication {
    name = "webtop-nix-reinstall";
    runtimeInputs = with pkgs; [ coreutils gnused jq config.nix.package home-manager libnotify git ];
    text = ''
      r=/persist/nix/reinstall
      export DBUS_SESSION_BUS_ADDRESS=${userBus}
      # Best effort and in the background: Plasma's notification server may not be up yet, and a
      # call to an unowned name can hang until the D-Bus timeout.
      notify() {
        (for _ in $(seq 1 12); do
          timeout 5 notify-send -a "Desktop" -i system-software-update "$1" "$2" 2>/dev/null && exit
          sleep 5
        done) &
      }
      notify "Desktop image updated" "Reinstalling your Nix packages in the background."
      failed=0
      manifest="$r/$USER.manifest.json"
      if [ -f "$manifest" ]; then
        # Manifest v2 has an array of elements, v3 a map. Installed by locked URL: same versions.
        refs=$(jq -r '.elements | to_entries[] | .value | select(.url != null and .attrPath != null)
                      | "\(.url)#\(.attrPath)"' "$manifest")
        for ref in $refs; do
          echo "reinstalling $ref"
          nix profile install "$ref" || { echo "failed: $ref"; failed=1; }
        done
        jq -r '.elements | to_entries[] | .value | select(.url == null) | .storePaths[]?' "$manifest" \
          | sed 's/^/not reinstallable (no flake reference): /'
      fi
      hm="$HOME/.config/home-manager"
      if [ -f "$hm/flake.nix" ]; then
        echo "home-manager switch --flake $hm"
        home-manager switch --flake "$hm" || failed=1
      fi
      if [ "$failed" = 0 ]; then
        notify "Packages reinstalled" "Your Nix packages are back."
      else
        notify "Package reinstall incomplete" "See: journalctl -u webtop-nix-reinstall"
      fi
      wait
      exit "$failed"
    '';
  };
in
{
  environment.systemPackages = [ agent ];

  # Boot progress until Plasma and Selkies are up, then client/idle reports. The controller waits
  # for its ready report (requireAgentReady), so it always restarts.
  systemd.services.webtop-agent = {
    description = "Desktop agent: boot progress and activity reports";
    wantedBy = [ "multi-user.target" ];
    after = [ "dbus.service" ];
    path = [ config.systemd.package ];
    serviceConfig = {
      ExecStart = "${exe} run";
      EnvironmentFile = envFile;
      Restart = "always";
      RestartSec = 3;
    };
  };

  # Records the stop with the controller and waits for its ack before PID 1 exits. A boot-time
  # RemainAfterExit unit whose ExecStop runs at shutdown: systemd unmounts the token, resolv.conf
  # and hosts early in the shutdown, and RequiresMountsFor stops this unit before that. The agent
  # derives the target from the queued jobs: SIGRTMIN+3 (pod deletion) queues halt.target, logind
  # and `systemctl poweroff` poweroff.target; that is how kube-stop and user stops differ.
  systemd.services.webtop-power-hook = {
    description = "Report the shutdown to the desktop controller";
    wantedBy = [ "multi-user.target" ];
    after = [ "dbus.service" ];
    unitConfig.RequiresMountsFor = "/run/webtop/sa /etc/resolv.conf /etc/hosts";
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${pkgs.coreutils}/bin/true";
      ExecStop = "${exe} power-hook";
      EnvironmentFile = envFile;
      StandardOutput = "journal+console"; # the journal relay may be gone by now
      TimeoutStopSec = 90; # the agent itself gives up after WEBTOP_POWER_HOOK_TIMEOUT (60s)
    };
  };

  # Saves the Plasma session on a pod deletion or a suspend, where ksmserver does not. Stopped before
  # the session and the user manager (plasma-ksmserver.service runs there).
  systemd.services.webtop-session-save = {
    description = "Save the Plasma session before an external stop";
    wantedBy = [ "multi-user.target" ];
    after = [ "webtop-session.service" "user@${uid}.service" ];
    environment = {
      DBUS_SESSION_BUS_ADDRESS = userBus;
      XDG_RUNTIME_DIR = "/run/user/${uid}";
    };
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      User = cfg.user;
      ExecStart = "${pkgs.coreutils}/bin/true";
      ExecStop = "${exe} save-session";
      EnvironmentFile = envFile;
      TimeoutStopSec = 40;
    };
  };

  # No real suspend in a pod: a suspend from the desktop saves the session and powers off with
  # reason "suspend". Hibernation variants are off.
  systemd.services.systemd-suspend = {
    path = [ config.systemd.package ];
    serviceConfig.ExecStart = [ "" "${exe} suspend" ];
  };
  systemd.sleep.settings.Sleep = {
    AllowHibernation = "no";
    AllowHybridSleep = "no";
    AllowSuspendThenHibernate = "no";
  };

  # Image updates (webtop-init): reinstall the recorded profile and run home-manager, then delete
  # the old store overlay.
  systemd.services.webtop-nix-reinstall = {
    description = "Reinstall the user's Nix packages after an image update";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [ "network-online.target" "nix-daemon.socket" "webtop-session.service" ];
    unitConfig.ConditionPathExists = "/persist/nix/reinstall/pending";
    serviceConfig = {
      # Not oneshot: multi-user.target (and so the agent's ready report) would wait for it.
      Type = "exec";
      User = cfg.user;
      ExecStart = lib.getExe reinstall;
      # "+": as root, the records belong to root. Only after success; a failure retries next boot.
      ExecStopPost = "+${pkgs.runtimeShell} -c '[ \"$$SERVICE_RESULT\" = success ] && ${pkgs.coreutils}/bin/rm -f /persist/nix/reinstall/pending || true'";
    };
  };
  systemd.services.webtop-nix-trash = {
    description = "Delete store overlays left by an image update";
    wantedBy = [ "multi-user.target" ];
    unitConfig.ConditionPathExistsGlob = "/persist/nix/trash-*";
    serviceConfig = {
      Type = "exec"; # see webtop-nix-reinstall
      ExecStart = "${pkgs.runtimeShell} -c '${pkgs.coreutils}/bin/rm -rf /persist/nix/trash-*'";
      Nice = 19;
      IOSchedulingClass = "idle";
    };
  };
}
