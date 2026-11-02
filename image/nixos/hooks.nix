# The in-pod agent (agent/) and the units that tie the desktop's power actions, boot progress and
# activity to the controller's power/report API (docs/contract.md, section 8).
{ config, lib, pkgs, agent, ... }:

let
  cfg = config.services.webtop;
  uid = toString cfg.uid;
  exe = "${agent}/bin/webtop-agent";
  envFile = "-/run/webtop/env";
  userBus = "unix:path=/run/user/${uid}/bus";

  # Best effort and in the background: Plasma's notification server may not be up yet, and a call
  # to an unowned name can hang until the D-Bus timeout. $NOTIFY_PREFIX runs it as the user (root).
  notifyFn = ''
    notify() {
      (for _ in $(seq 1 12); do
        # shellcheck disable=SC2086
        timeout 5 ''${NOTIFY_PREFIX:-} notify-send -a "Desktop" -i system-software-update "$1" "$2" 2>/dev/null && exit
        sleep 5
      done) &
    }
  '';

  restore = pkgs.writeShellApplication {
    name = "webtop-nix-restore";
    runtimeInputs = with pkgs; [ coreutils findutils gnugrep config.nix.package ];
    text = builtins.readFile ../webtop-nix-restore.sh;
  };

  # Root. Any restore exit code is fine: what did not come back is left to the reinstall.
  restoreUnit = pkgs.writeShellApplication {
    name = "webtop-nix-restore-unit";
    runtimeInputs = with pkgs; [ coreutils util-linux libnotify systemd restore ];
    text = ''
      export NOTIFY_PREFIX="runuser -u ${cfg.user} -- env DBUS_SESSION_BUS_ADDRESS=${userBus}"
      ${notifyFn}
      notify "Desktop image updated" "Restoring your Nix packages in the background."
      webtop-nix-restore || echo "webtop-nix-restore exit $?"
      # Not reached when the unit is stopped: the marker stays and the restore resumes next boot.
      rm -f /persist/nix/reinstall/restore
      systemctl start --no-block webtop-nix-trash.service webtop-nix-reinstall.service
      wait
    '';
  };

  reinstall = pkgs.writeShellApplication {
    name = "webtop-nix-reinstall";
    runtimeInputs = with pkgs; [ coreutils gnused jq config.nix.package libnotify ];
    text = ''
      r=/persist/nix/reinstall
      export DBUS_SESSION_BUS_ADDRESS=${userBus}
      ${notifyFn}
      failed=0
      profiles="$HOME/.local/state/nix/profiles"
      old="$profiles.old-$(date +%s)"
      # Dangling links (their generations were not restored) are moved aside: nix cannot extend them.
      aside() {
        mkdir -p "$old"
        for l in "$@"; do
          if [ -L "$l" ]; then mv "$l" "$old/"; fi
        done
      }

      # The declaration's versions (ADR 0013) are only a history; dangling ones are moved aside.
      if [ -L "$profiles/declaration" ] && [ ! -e "$profiles/declaration" ]; then
        aside "$profiles/declaration" "$profiles"/declaration-*-link
      fi

      p="$profiles/profile"
      manifest="$r/$USER.manifest.json"
      if [ -L "$p" ] && [ -e "$p" ]; then
        echo "nix profile restored"
      else
        if [ -L "$p" ]; then
          aside "$p" "$profiles"/profile-*-link
        fi
        if [ -L "$HOME/.nix-profile" ] && [ ! -e "$HOME/.nix-profile" ]; then rm -f "$HOME/.nix-profile"; fi
        if [ -f "$manifest" ]; then
          # Manifest v2 has an array of elements, v3 a map. Installed by locked URL: same versions.
          refs=$(jq -r '.elements | to_entries[] | .value | select(.url != null and .attrPath != null)
                        | "\(.url)#\(.attrPath)"' "$manifest")
          # Into the profile, where they were: declared packages come back with the switch below.
          for ref in $refs; do
            echo "reinstalling $ref"
            nix profile add --profile-backend profile "$ref" || { echo "failed: $ref"; failed=1; }
          done
          jq -r '.elements | to_entries[] | .value | select(.url == null) | .storePaths[]?' "$manifest" \
            | sed 's/^/not reinstallable (no flake reference): /'
        fi
      fi

      # home-manager: relock, switch (and on the first such boot, move to packages.json).
      ${config.systemd.services.webtop-home-manager.serviceConfig.ExecStart} || failed=1

      if [ "$failed" = 0 ]; then
        notify "Packages restored" "Your Nix packages are back."
      else
        notify "Package reinstall incomplete" "See: journalctl -u webtop-nix-reinstall"
      fi
      wait
      exit "$failed"
    '';
  };
in
{
  # kioclient starts the app of /apps/<name> from its desktop file (agent/internal/apps); Plasma
  # already pulls it in, listed so that stays true. webtop-app-screen comes with the webtop module.
  environment.systemPackages = [ agent pkgs.kdePackages.kde-cli-tools ];

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
      User = cfg.user;
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
    after = [ "network-online.target" "nix-daemon.socket" "webtop-session.service" ];
    unitConfig.ConditionPathExists = [
      "/persist/nix/reinstall/pending"
      "!/persist/nix/reinstall/restore"
    ];
    serviceConfig = {
      Type = "exec"; # see webtop-nix-restore
      User = cfg.user;
      ExecStart = lib.getExe reinstall;
      TimeoutStopSec = 10;
      # "+": as root, the records belong to root. Only after success; a failure retries next boot.
      ExecStopPost = "+${pkgs.runtimeShell} -c '[ \"$$SERVICE_RESULT\" = success ] && ${pkgs.coreutils}/bin/rm -f /persist/nix/reinstall/pending || true'";
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
