# home-manager for the desktop user: the image's copy resolves from the registry, a starter flake is
# created on first boot, and its generation (for the placeholder account) is prebuilt into the image.
{ config, lib, pkgs, nixpkgs, home-manager, flake-registry, ... }:

let
  cfg = config.services.webtop;
  system = pkgs.stdenv.hostPlatform.system;
  # toFile, not replaceVars: home.nix is also imported below, which needs a plain path (no IFD).
  render = vars: name: builtins.toFile name (lib.replaceStrings
    (map (n: "@${n}@") (lib.attrNames vars)) (lib.attrValues vars)
    (builtins.readFile ../home-manager/${name}));
  # @user@ and @home@ are the login name, known only at runtime (identity.nix).
  runtimeFile = render { inherit (config.system) stateVersion; };
  # The user's home on the volume, whatever paths it is mounted at (webtop-init).
  home = "/persist/home/user";
  dir = "${home}/.config/home-manager";
  marker = "${home}/.local/state/webtop/home-manager-template";
  notice = "${home}/.local/state/webtop/home-manager-notice";
  templateTool = pkgs.writeShellApplication {
    name = "webtop-template";
    runtimeInputs = with pkgs; [ coreutils diffutils gnugrep ];
    text = builtins.readFile ../webtop-template.sh;
  };
  pinned = [ "nixpkgs" "home-manager" ];
  template = home-manager.lib.homeManagerConfiguration {
    pkgs = nixpkgs.legacyPackages.${system};
    modules = [ (render { inherit (config.system) stateVersion; user = cfg.user; home = "/home/${cfg.user}"; } "home.nix") ];
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
  environment.systemPackages = [ home-manager.packages.${system}.home-manager templateTool ];

  # The generation and news file of the unchanged template for the placeholder account. A user with
  # another login name reuses their closure; only the few derivations carrying the name are built.
  system.extraDependencies = [ template.activationPackage template.config.news.json.output ];

  # Creates the starter flake once (the marker keeps it from coming back after the user deletes it),
  # and at every start rewrites it for the current login name while it is unmodified: what this unit
  # last wrote, or the template rendered for a name in the home paths. An edited flake that still
  # names another user is left alone; the journal and a notification list the lines to change.
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
          tmp=$(mktemp -d)
          trap 'rm -rf "$tmp"' EXIT
          # render <name> <dir>: the template for that login name.
          render() {
            mkdir -p "$2"
            sed -e "s|@user@|$1|g" -e "s|@home@|/home/$1|g" ${runtimeFile "flake.nix"} > "$2/flake.nix"
            sed -e "s|@user@|$1|g" -e "s|@home@|/home/$1|g" ${runtimeFile "home.nix"} > "$2/home.nix"
          }
          render "$USER" "$tmp/new"
          rm -f "$notice"

          if [ ! -e "$dir" ]; then
            [ ! -e ${marker} ] || exit 0
            webtop-template write "$dir/flake.nix" "$tmp/new/flake.nix"
            webtop-template write "$dir/home.nix" "$tmp/new/home.nix"
            mkdir -p ${builtins.dirOf marker} && touch ${marker}
            exit 0
          fi
          if cmp -s "$dir/flake.nix" "$tmp/new/flake.nix" && cmp -s "$dir/home.nix" "$tmp/new/home.nix"; then
            exit 0
          fi

          for p in ''${WEBTOP_HOME_PATHS:-}; do
            n=''${p#/home/}
            [ "$n" = "$USER" ] || render "$n" "$tmp/old/$n"
          done
          shopt -s nullglob
          if webtop-template unmodified "$dir/flake.nix" "$tmp"/old/*/flake.nix &&
             webtop-template unmodified "$dir/home.nix" "$tmp"/old/*/home.nix; then
            webtop-template write "$dir/flake.nix" "$tmp/new/flake.nix"
            webtop-template write "$dir/home.nix" "$tmp/new/home.nix"
            echo "rewrote the starter flake in ~/.config/home-manager for $USER ($HOME)"
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
    environment.DBUS_SESSION_BUS_ADDRESS = "unix:path=/run/user/${toString cfg.uid}/bus";
    serviceConfig = {
      Type = "exec"; # not oneshot: multi-user.target must not wait for the session
      User = toString cfg.uid;
      ExecStart = lib.getExe (pkgs.writeShellApplication {
        name = "webtop-home-manager-notice";
        runtimeInputs = with pkgs; [ coreutils libnotify ];
        text = ''
          echo "notifying: $(head -n1 ${notice})"
          # Plasma's notification server may not be up yet.
          for _ in $(seq 1 24); do
            timeout 5 notify-send -a Desktop -i dialog-warning "Update your home-manager flake" \
              "$(tail -n +2 ${notice}) See: journalctl -u webtop-home-manager-template" 2>/dev/null && exit 0
            sleep 5
          done
          echo "notification not delivered"
        '';
      });
    };
  };
}
