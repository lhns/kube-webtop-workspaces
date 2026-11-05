# The in-pod agent (agent/) and the units that tie the desktop's power actions, boot progress and
# activity to the controller's power/report API (docs/contract.md, section 8).
{ config, lib, pkgs, agent, ... }:

let
  cfg = config.services.webtop;
  uid = toString cfg.uid;
  exe = "${agent}/bin/webtop-agent";
  envFile = "-/run/webtop/env";
  userBus = "unix:path=/run/user/${uid}/bus";

  inherit (cfg.nixProfile) notify progress reinstall;

  # The "application" of the restore's tray entry: the shell reads its name and icon from here.
  restoreDesktop = pkgs.makeDesktopItem {
    name = "webtop-restore";
    desktopName = "Software restore";
    icon = "system-software-update";
    exec = "true";
    noDisplay = true;
  };

  restore = pkgs.writeShellApplication {
    name = "webtop-nix-restore";
    runtimeInputs = with pkgs; [ coreutils findutils gnugrep config.nix.package ];
    text = builtins.readFile ../webtop-nix-restore.sh;
  };

  # Root. Any restore exit code is fine: what did not come back is left to the reinstall.
  restoreUnit = pkgs.writeShellApplication {
    name = "webtop-nix-restore-unit";
    runtimeInputs = with pkgs; [ coreutils systemd restore ];
    text = ''
      webtop-nix-restore || echo "webtop-nix-restore exit $?"
      # Not reached when the unit is stopped: the marker stays and the restore resumes next boot.
      rm -f /persist/nix/reinstall/restore
      systemctl start --no-block webtop-nix-trash.service webtop-nix-reinstall.service
      wait
    '';
  };
in
{
  # kioclient starts the app of /apps/<name> from its desktop file (agent/internal/apps); Plasma
  # already pulls it in, listed so that stays true. webtop-app-screen comes with the webtop module.
  environment.systemPackages = [ agent pkgs.kdePackages.kde-cli-tools progress notify restoreDesktop ];

  # The restore scripts run as the user and replace their progress file atomically, so it gets a
  # directory of its own: /run/webtop is root's.
  systemd.tmpfiles.rules = [ "d /run/webtop/restore 0755 ${uid} - -" ];

  # The restore's progress as a job in the tray, from the user's session bus.
  systemd.services.webtop-restore-job = {
    description = "Show the software restore as a job in the tray";
    wantedBy = [ "multi-user.target" ];
    after = [ "webtop-session.service" ];
    environment = {
      DBUS_SESSION_BUS_ADDRESS = userBus;
      XDG_RUNTIME_DIR = "/run/user/${uid}";
    };
    serviceConfig = {
      Type = "exec"; # not oneshot: multi-user.target must not wait for the session
      User = uid;
      ExecStart = "${exe} restore-job";
      EnvironmentFile = envFile;
      Restart = "always";
      RestartSec = 5;
    };
  };

  # Boot progress until Plasma and Selkies are up, then client/idle reports, plus the gateway's apps
  # API on :8081. The controller waits for its ready report (requireAgentReady), so it always restarts.
  systemd.services.webtop-agent = {
    description = "Desktop agent: boot progress, activity reports, apps API";
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

  # A session that ends by itself (the user logged out; webtop-nix restarts it) leaves a marker the
  # agent reports, so the gateway can sign the browser out at the login provider. The agent skips
  # it when poweroff, reboot or halt is queued: those are shutdowns (power-hook). "+": as root,
  # because the marker is in /run/webtop; the agent reads only two booleans of the user's
  # ~/.config/webtop/session.conf. Without gateway.logoutUrl the controller records nothing.
  systemd.services.webtop-session.serviceConfig.ExecStopPost = [ "+${exe} session-exit" ];

  # Saves the Plasma session on a pod deletion or a fallback suspend, where ksmserver does not.
  # Stopped before the session and the user manager (plasma-ksmserver.service runs there). The
  # thaw first ("+": as root) is a no-op unless Sleep left units frozen, which would hang ksmserver.
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
      User = uid;
      ExecStart = "${pkgs.coreutils}/bin/true";
      ExecStop = [ "+${exe} thaw" "${exe} save-session" ];
      EnvironmentFile = envFile;
      TimeoutStopSec = 40;
    };
  };

  # No real suspend in a pod: Sleep freezes the session (ADR 0009). The agent blocks until the next
  # web client, so no start timeout. After= webtop-session-save stops this first, so the agent
  # thaws before ksmserver is asked to save. Hibernation variants are off.
  systemd.services.systemd-suspend = {
    path = [ config.systemd.package ];
    after = [ "webtop-session-save.service" ];
    serviceConfig = {
      ExecStart = [ "" "${exe} suspend" ];
      EnvironmentFile = envFile;
      TimeoutStartSec = "infinity";
    };
  };
  systemd.sleep.settings.Sleep = {
    AllowHibernation = "no";
    AllowHybridSleep = "no";
    AllowSuspendThenHibernate = "no";
  };

  # Image updates (webtop-init keeps the old store overlay in trash-*): the restore unit copies the
  # users' paths from it into the new store, then removes the marker and starts the trash cleanup
  # and the reinstall, which covers what did not come back. All three wait for the marker to go.
  systemd.services.webtop-nix-restore = {
    description = "Restore the user's Nix store paths after an image update";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [ "network-online.target" "nix-daemon.socket" ];
    unitConfig.ConditionPathExists = "/persist/nix/reinstall/restore";
    serviceConfig = {
      # Not oneshot: multi-user.target (and so the agent's ready report) would wait for it.
      Type = "exec";
      ExecStart = lib.getExe restoreUnit;
      Nice = 10;
      # A stopped restore must not eat the termination grace period; it resumes next boot.
      TimeoutStopSec = 10;
    };
  };
  systemd.services.webtop-nix-reinstall = {
    description = "Reinstall the user's Nix packages after an image update";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [ "network-online.target" "nix-daemon.socket" "nix-daemon.service" "webtop-session.service" ];
    unitConfig.ConditionPathExists = [
      "/persist/nix/reinstall/pending"
      "!/persist/nix/reinstall/restore"
    ];
    environment = {
      DBUS_SESSION_BUS_ADDRESS = userBus;
      XDG_RUNTIME_DIR = "/run/user/${uid}";
    };
    serviceConfig = {
      Type = "exec"; # see webtop-nix-restore
      User = uid;
      # The manifest is named after the home's directory on the volume (webtop-init), not the login name.
      ExecStart = "${lib.getExe reinstall} /persist/nix/reinstall/user.manifest.json";
      # As webtop-home-manager.service, whose setup script it runs.
      TimeoutStopSec = "5min";
      # "+": as root, the records belong to root. Only after the script ran to the end: a failure or a
      # stop retries next boot (systemd counts a SIGTERM death as success, hence EXIT_CODE).
      ExecStopPost = "+${pkgs.runtimeShell} -c '[ \"$$SERVICE_RESULT\" = success ] && [ \"$$EXIT_CODE\" = exited ] && ${pkgs.coreutils}/bin/rm -f /persist/nix/reinstall/pending || true'";
    };
  };
  systemd.services.webtop-nix-trash = {
    description = "Delete store overlays left by an image update";
    wantedBy = [ "multi-user.target" ];
    unitConfig = {
      ConditionPathExistsGlob = "/persist/nix/trash-*";
      ConditionPathExists = "!/persist/nix/reinstall/restore"; # the restore reads from them
    };
    serviceConfig = {
      Type = "exec"; # see webtop-nix-restore
      ExecStart = "${pkgs.runtimeShell} -c '${pkgs.coreutils}/bin/rm -rf /persist/nix/trash-*'";
      Nice = 19;
      IOSchedulingClass = "idle";
    };
  };
}
