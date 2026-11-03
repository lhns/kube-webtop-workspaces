# home-manager for the desktop user: the image's copy resolves from the registry, a starter flake is
# created on first boot, and its generation (for the placeholder account) is prebuilt into the image.
# It also owns software installation (ADR 0013): the patched `nix profile` records packages in the
# flake's packages.json.
{ config, lib, pkgs, nixpkgs, home-manager, flake-registry, ... }:

let
  cfg = config.services.webtop;
  system = pkgs.stdenv.hostPlatform.system;
  # toFile, not replaceVars: home.nix is also imported below, which needs a plain path (no IFD).
  renderFile = vars: src: name: builtins.toFile name (lib.replaceStrings
    (map (n: "@${n}@") (lib.attrNames vars)) (lib.attrValues vars)
    (builtins.readFile src));
  render = vars: name: renderFile vars ../home-manager/${name} name;
  # @user@ and @home@ are the login name, known only at runtime (identity.nix).
  runtimeFile = render { inherit (config.system) stateVersion; };
  # Earlier starter files (before packages.nix): unmodified ones are rewritten too.
  legacyFile = name: renderFile { inherit (config.system) stateVersion; } ../home-manager/legacy/${name} "legacy-${name}";
  # The user's home on the volume, whatever paths it is mounted at (webtop-init).
  home = "/persist/home/user";
  dir = "${home}/.config/home-manager";
  stateDir = "${home}/.local/state/webtop";
  marker = "${stateDir}/home-manager-template";
  notice = "${stateDir}/home-manager-notice";
  templateTool = pkgs.writeShellApplication {
    name = "webtop-template";
    runtimeInputs = with pkgs; [ coreutils diffutils gnugrep ];
    text = builtins.readFile ../webtop-template.sh;
  };
  pinned = [ "nixpkgs" "home-manager" ];
  template = home-manager.lib.homeManagerConfiguration {
    pkgs = nixpkgs.legacyPackages.${system};
    modules = [
      (render { inherit (config.system) stateVersion; user = cfg.user; home = "/home/${cfg.user}"; } "home.nix")
      ../home-manager/packages.nix
    ];
  };
  hmPackage = home-manager.packages.${system}.home-manager;
  notify = import ../notify.nix { inherit pkgs; uid = toString cfg.uid; };
  progress = import ../restore-progress.nix { inherit pkgs; };
  # Which homeConfigurations to switch: setup script and profile hook.
  hmAttr = pkgs.writeShellApplication {
    name = "webtop-hm-attr";
    runtimeInputs = with pkgs; [ coreutils jq config.nix.package ];
    text = builtins.readFile ../webtop-hm-attr.sh;
  };

  # profile-hook (packages.nix): applies a changed packages.json.
  profileHook = pkgs.writeShellApplication {
    name = "webtop-profile-hook";
    runtimeInputs = [ pkgs.coreutils pkgs.git config.nix.package hmPackage hmAttr ];
    text = builtins.readFile ../webtop-profile-hook.sh;
  };

  setup = pkgs.writeShellApplication {
    name = "webtop-home-manager";
    runtimeInputs = with pkgs; [ coreutils gnused gnugrep jq git config.nix.package hmPackage hmAttr notify progress ];
    text = builtins.readFile ../webtop-home-manager.sh;
  };
  upstream = lib.importJSON "${flake-registry}/flake-registry.json";
in
{
  # CLI and modules from one source; the template's `home-manager` input resolves to it.
  nix.registry.home-manager.flake = home-manager;
  # Flake inputs resolve indirect refs through the global registry only, not nix.registry, so the
  # image's pins replace the upstream global registry's entries for them (offline, no download).
  nix.settings.flake-registry = pkgs.writeText "flake-registry.json" (builtins.toJSON {
    version = 2;
    flakes = map (id: { from = { type = "indirect"; inherit id; }; to = config.nix.registry.${id}.to; }) pinned
      ++ lib.filter (e: !lib.elem e.from.id pinned) upstream.flakes;
  });
  # webtop-template: also for other units that update files the image wrote into the home.
  environment.systemPackages = [ hmPackage templateTool profileHook ];

  # `nix profile` with backends (ADR 0013).
  nix.package = nixpkgs.legacyPackages.${system}.callPackage ../nix { };

  # The generation and news file of the unchanged template for the placeholder account. A user with
  # another login name reuses their closure; only the few derivations carrying the name are built.
  system.extraDependencies = [ template.activationPackage template.config.news.json.output ];

  # Creates the starter flake once (the marker keeps it from coming back after the user deletes it),
  # and at every start rewrites it for the current login name while it is unmodified: what this unit
  # last wrote, or the template (also an earlier one without packages.nix) rendered for a name in the
  # home paths. An unmodified packages.nix follows the image. An edited flake that still names another
  # user is left alone; the journal and a notification list the lines to change. When it writes, the
  # setup unit below switches; when it adds packages.nix, that unit also imports the `nix profile`
  # packages into packages.json.
  systemd.services.webtop-home-manager-template = {
    description = "Create or rename the starter home-manager flake";
    wantedBy = [ "multi-user.target" ];
    before = [ "webtop-session.service" "webtop-nix-reinstall.service" ];
    serviceConfig = {
      Type = "oneshot";
      User = toString cfg.uid;
      EnvironmentFile = "-/run/webtop/env"; # WEBTOP_HOME_PATHS
      # USER and HOME come from passwd: the login name and /home/<name>.
      ExecStart = lib.getExe (pkgs.writeShellApplication {
        name = "webtop-home-manager-template";
        runtimeInputs = with pkgs; [ coreutils diffutils gnused gnugrep templateTool ];
        text = ''
          dir=${dir}
          notice=${notice}
          state=${stateDir}
          tmp=$(mktemp -d)
          trap 'rm -rf "$tmp"' EXIT
          sub() { sed -e "s|@user@|$1|g" -e "s|@home@|/home/$1|g" "$2" > "$3"; }
          # render <name> <dir>: the template for that login name.
          render() {
            mkdir -p "$2"
            sub "$1" ${runtimeFile "flake.nix"} "$2/flake.nix"
            sub "$1" ${runtimeFile "home.nix"} "$2/home.nix"
          }
          # legacy <name> <dir>: the earlier templates for that login name.
          legacy() {
            mkdir -p "$2"
            sub "$1" ${legacyFile "flake-1.nix"} "$2/flake-1.nix"
            sub "$1" ${legacyFile "flake-2.nix"} "$2/flake-2.nix"
            sub "$1" ${legacyFile "home.nix"} "$2/home.nix"
          }
          # Starter files that come with packages.nix; packages.json is the user's from then on.
          add_packages() {
            webtop-template write "$dir/packages.nix" ${../home-manager/packages.nix}
            [ -e "$dir/packages.json" ] || webtop-template write "$dir/packages.json" ${../home-manager/packages.json}
          }
          # packages.nix writes ~/.config/nix/nix.conf; one of the user's own stays theirs.
          own_nix_conf() {
            c=${home}/.config/nix/nix.conf
            { [ -e "$c" ] || [ -L "$c" ]; } && ! grep -qs '^profile-backend = declaration:' "$c"
          }
          render "$USER" "$tmp/new"
          rm -f "$notice"
          mkdir -p "$state"

          if [ ! -e "$dir" ]; then
            [ ! -e ${marker} ] || exit 0
            webtop-template write "$dir/flake.nix" "$tmp/new/flake.nix"
            webtop-template write "$dir/home.nix" "$tmp/new/home.nix"
            add_packages
            touch ${marker} "$state/home-manager-switch"
            exit 0
          fi
          if [ -e "$dir/packages.nix" ] && ! cmp -s "$dir/packages.nix" ${../home-manager/packages.nix} &&
             webtop-template unmodified "$dir/packages.nix"; then
            webtop-template write "$dir/packages.nix" ${../home-manager/packages.nix}
            touch "$state/home-manager-switch"
          fi
          if cmp -s "$dir/flake.nix" "$tmp/new/flake.nix" && cmp -s "$dir/home.nix" "$tmp/new/home.nix"; then
            exit 0
          fi

          legacy "$USER" "$tmp/legacy/$USER"
          for p in ''${WEBTOP_HOME_PATHS:-}; do
            n=''${p#/home/}
            [ "$n" = "$USER" ] || { render "$n" "$tmp/old/$n"; legacy "$n" "$tmp/legacy/$n"; }
          done
          shopt -s nullglob
          if webtop-template unmodified "$dir/flake.nix" "$tmp"/old/*/flake.nix "$tmp"/legacy/*/flake-*.nix &&
             webtop-template unmodified "$dir/home.nix" "$tmp"/old/*/home.nix "$tmp"/legacy/*/home.nix; then
            if [ ! -e "$dir/packages.nix" ] && own_nix_conf; then
              # Rename only: the earlier template, without packages.nix; already done is a no-op.
              cur=$tmp/legacy/$USER
              if cmp -s "$dir/flake.nix" "$cur/flake-2.nix" && cmp -s "$dir/home.nix" "$cur/home.nix"; then exit 0; fi
              webtop-template write "$dir/flake.nix" "$cur/flake-2.nix"
              webtop-template write "$dir/home.nix" "$cur/home.nix"
              echo "rewrote the starter flake for $USER; kept without packages.nix: ~/.config/nix/nix.conf is the user's"
            else
              if [ ! -e "$dir/packages.nix" ]; then
                add_packages
                touch "$state/packages-json-import"
              fi
              webtop-template write "$dir/flake.nix" "$tmp/new/flake.nix"
              webtop-template write "$dir/home.nix" "$tmp/new/home.nix"
              echo "rewrote the starter flake in ~/.config/home-manager for $USER ($HOME)"
            fi
            touch "$state/home-manager-switch"
            exit 0
          fi

          # Edited: point at the lines that still name another user, with their replacement.
          lines=$(cd "$dir" && grep -nE 'homeConfigurations\."[^"]*"|home\.(username|homeDirectory) *= *"' flake.nix home.nix 2>/dev/null |
            grep -vF -e "\"$USER\"" -e "\"$HOME\"" || true)
          [ -n "$lines" ] || exit 0
          mkdir -p "$(dirname "$notice")"
          {
            echo "The flake in ~/.config/home-manager names another user; change these lines, then run home-manager switch:"
            while IFS= read -r l; do
              file=''${l%%:*} num=''${l#*:} old=''${l#*:*:}
              num=''${num%%:*} old=''${old#"''${old%%[! ]*}"}
              new=$(printf '%s\n' "$old" | sed -E \
                -e "s/homeConfigurations\.\"[^\"]*\"/homeConfigurations.\"$USER\"/" \
                -e "s|(home\.username *= *)\"[^\"]*\"|\1\"$USER\"|" \
                -e "s|(home\.homeDirectory *= *)\"[^\"]*\"|\1\"$HOME\"|")
              echo "  $file:$num: $old  ->  $new"
            done <<< "$lines"
          } | tee "$notice"
        '';
      });
    };
  };

  # The notification for the above, once Plasma (its notification server) is up.
  systemd.services.webtop-home-manager-notice = {
    description = "Tell the user which home-manager lines name another user";
    wantedBy = [ "multi-user.target" ];
    after = [ "webtop-session.service" "webtop-home-manager-template.service" ];
    unitConfig.ConditionPathExists = notice;
    environment.WEBTOP_NOTIFY_TRIES = "24"; # Plasma may take a while after the session starts
    serviceConfig = {
      Type = "exec"; # not oneshot: multi-user.target must not wait for the session
      User = toString cfg.uid;
      ExecStart = lib.getExe (pkgs.writeShellApplication {
        name = "webtop-home-manager-notice";
        runtimeInputs = with pkgs; [ coreutils notify ];
        text = ''
          echo "notifying: $(head -n1 ${notice})"
          webtop-notify "Update your home-manager flake" \
            "$(tail -n +2 ${notice}) See: journalctl -u webtop-home-manager-template" dialog-warning
        '';
      });
    };
  };

  # Import, relock and switch (image/webtop-home-manager.sh), after the template unit. After an image
  # update the reinstall unit runs it instead, once the store paths are restored.
  systemd.services.webtop-home-manager = {
    description = "Bring the user's home-manager setup to the image's";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    # nix-daemon.service too: at shutdown the daemon must outlive a switch the stop waits for.
    after = [ "network-online.target" "nix-daemon.socket" "nix-daemon.service" "webtop-session.service" "webtop-home-manager-template.service" ];
    unitConfig.ConditionPathExists = "!/persist/nix/reinstall/pending";
    environment = {
      DBUS_SESSION_BUS_ADDRESS = "unix:path=/run/user/${toString cfg.uid}/bus";
      XDG_RUNTIME_DIR = "/run/user/${toString cfg.uid}";
    };
    serviceConfig = {
      # Not oneshot: multi-user.target (and so the agent's ready report) would wait for a switch.
      Type = "exec";
      User = toString cfg.uid;
      ExecStart = lib.getExe setup;
      # A stop during the import's switch waits for it (webtop-home-manager.sh); the chart's
      # terminationGracePeriodSeconds must cover this plus the session save.
      TimeoutStopSec = "5min";
    };
  };
}
