# The in-pod agent (agent/) and the units that tie the desktop's power actions, boot progress and
# activity to the controller's power/report API (docs/contract.md, section 8).
{ config, pkgs, agent, ... }:

let
  cfg = config.services.webtop;
  uid = toString cfg.uid;
  exe = "${agent}/bin/webtop-agent";
  envFile = "-/run/webtop/env";
  userBus = "unix:path=/run/user/${uid}/bus";

  inherit (cfg.nixProfile) notify progress;

  # The "application" of the restore's tray entry: the shell reads its name and icon from here.
  restoreDesktop = pkgs.makeDesktopItem {
    name = "webtop-restore";
    desktopName = "Software restore";
    icon = "system-software-update";
    exec = "true";
    noDisplay = true;
  };
in
{
  # kioclient starts the app of /apps/<name> from its desktop file (agent/internal/apps); Plasma
  # already pulls it in, listed so that stays true. webtop-app-screen comes with the webtop module.
  environment.systemPackages = [ agent pkgs.kdePackages.kde-cli-tools progress notify restoreDesktop ];

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
  services.webtop.sessionStopCommands = [ "+${exe} session-exit" ];

  # Sleep freezes the session (ADR 0019). WEBTOP_SUSPEND_MODE=poweroff in webtop-init's env file
  # powers off instead, with stop reason "suspend" (/run/webtop/stop-reason), which the power hook
  # reports. The hook tells the controller asleep/awake.
  services.webtop.sleep = {
    mode = "freeze";
    environmentFiles = [ envFile ];
    hooks = [ "${exe} sleep-report" ];
  };
  # The session save on a pod deletion (services.webtop.sessionSave, on in containers) is stopped
  # before the session: the chart's terminationGracePeriodSeconds must cover its 40 s TimeoutStopSec.
}
