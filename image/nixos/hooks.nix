# The in-pod agent (agent/) and the units that tie the desktop's power actions, boot progress and
# activity to the controller's power/report API (docs/contract.md, section 8).
{ config, lib, pkgs, agent, home-manager, ... }:

let
  cfg = config.services.webtop;
  uid = toString cfg.uid;
  exe = "${agent}/bin/webtop-agent";
  envFile = "-/run/webtop/env";
  userBus = "unix:path=/run/user/${uid}/bus";

  hmPackage = home-manager.packages.${pkgs.stdenv.hostPlatform.system}.home-manager;

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
    runtimeInputs = with pkgs; [ coreutils gnused jq config.nix.package hmPackage libnotify git ];
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
          for ref in $refs; do
            echo "reinstalling $ref"
            nix profile install "$ref" || { echo "failed: $ref"; failed=1; }
          done
          jq -r '.elements | to_entries[] | .value | select(.url == null) | .storePaths[]?' "$manifest" \
            | sed 's/^/not reinstallable (no flake reference): /'
        fi
      fi

      hm="$HOME/.config/home-manager"
      if [ -f "$hm/flake.nix" ]; then
        # Root inputs resolved through the image's registry are locked to the old image's store
        # paths; relock those that are gone.
        stale=()
        if [ -f "$hm/flake.lock" ]; then
          while IFS=$'\t' read -r name path; do
            if [ ! -e "$path" ]; then stale+=("$name"); fi
          done < <(jq -r '. as $l | .nodes.root.inputs | to_entries[] | select(.value | type == "string")
                          | .key as $k | $l.nodes[.value]
                          | select(.original.type == "indirect" and .locked.type == "path")
                          | [$k, .locked.path] | @tsv' "$hm/flake.lock")
        fi
        if [ "''${#stale[@]}" -gt 0 ]; then
          echo "relocking stale inputs: ''${stale[*]}"
          nix flake update --flake "$hm" "''${stale[@]}" || failed=1
        fi
        g="$profiles/home-manager"
        if [ -L "$g" ] && [ -e "$g" ]; then
          echo "home-manager generation restored"
        else
          aside "$g" "$profiles"/home-manager-*-link
          echo "home-manager switch --flake $hm"
          home-manager switch --flake "$hm" || failed=1
        fi
      fi

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
