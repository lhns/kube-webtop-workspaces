# home-manager for the desktop user: the image's copy and its `webtop` flake resolve from the registry,
# a starter flake is created on first boot, and its generation (for the placeholder account) is
# prebuilt into the image. It also owns software installation (ADR 0013): the patched `nix profile`
# records packages in the flake's packages.json, which the `webtop` flake's module installs.
{ config, lib, pkgs, nixpkgs, home-manager, flake-registry, ... }:

let
  cfg = config.services.webtop;
  system = pkgs.stdenv.hostPlatform.system;
  # toFile, not replaceVars: home.nix is also imported below, which needs a plain path (no IFD).
  render = vars: name: builtins.toFile name (lib.replaceStrings
    (map (n: "@${n}@") (lib.attrNames vars)) (lib.attrValues vars)
    (builtins.readFile ../home-manager/${name}));
  # @user@ and @home@ are the login name, known only at runtime (identity.nix).
  runtimeFile = render { inherit system; inherit (config.system) stateVersion; };
  # The user's home on the volume, whatever paths it is mounted at (webtop-init).
  home = "/persist/home/user";
  stateDir = "${home}/.local/state/webtop";
  notice = "${stateDir}/home-manager-notice";
  templateTool = pkgs.writeShellApplication {
    name = "webtop-template";
    runtimeInputs = with pkgs; [ coreutils diffutils gnugrep gnused ];
    text = builtins.readFile ../webtop-template.sh;
  };
  # The image's module flake: a store path in the registry like nixpkgs.
  webtopFlake = ../webtop-flake;
  pinned = [ "nixpkgs" "home-manager" "webtop" ];
  template = home-manager.lib.homeManagerConfiguration {
    pkgs = nixpkgs.legacyPackages.${system};
    modules = [
      (render { inherit system; inherit (config.system) stateVersion; user = cfg.user; home = "/home/${cfg.user}"; } "home.nix")
      (import ../webtop-flake/packages.nix ../home-manager/packages.json)
    ];
  };
  hmPackage = home-manager.packages.${system}.home-manager;
  notify = import ../notify.nix { inherit pkgs; uid = toString cfg.uid; };
  progress = import ../restore-progress.nix { inherit pkgs; };
  # profile-hook (the packages module): applies a changed packages.json.
  profileHook = pkgs.writeShellApplication {
    name = "webtop-profile-hook";
    runtimeInputs = [ pkgs.coreutils pkgs.git config.nix.package hmPackage ];
    text = builtins.readFile ../webtop-profile-hook.sh;
  };

  setup = pkgs.writeShellApplication {
    name = "webtop-home-manager";
    runtimeInputs = with pkgs; [ coreutils jq git config.nix.package hmPackage notify progress ];
    text = builtins.readFile ../webtop-home-manager.sh;
  };
  updateTool = pkgs.writeShellApplication {
    name = "webtop-home-manager-update";
    runtimeInputs = with pkgs; [ coreutils diffutils git config.nix.package templateTool hmPackage ];
    text = builtins.readFile ../webtop-home-manager-update.sh;
  };
  upstream = lib.importJSON "${flake-registry}/flake-registry.json";
in
{
  # CLI and modules from one source; the template's `home-manager` input resolves to it.
  nix.registry.home-manager.flake = home-manager;
  nix.registry.webtop.to = { type = "path"; path = "${webtopFlake}"; };
  # Flake inputs resolve indirect refs through the global registry only, not nix.registry, so the
  # image's pins replace the upstream global registry's entries for them (offline, no download).
  nix.settings.flake-registry = pkgs.writeText "flake-registry.json" (builtins.toJSON {
    version = 2;
    flakes = map (id: { from = { type = "indirect"; inherit id; }; to = config.nix.registry.${id}.to; }) pinned
      ++ lib.filter (e: !lib.elem e.from.id pinned) upstream.flakes;
  });
  # webtop-template: also for other units that update files the image wrote into the home.
  environment.systemPackages = [ hmPackage templateTool updateTool profileHook ];
  # The starter template for webtop-template render and webtop-home-manager-update.
  environment.etc."webtop/home-manager/flake.nix".source = runtimeFile "flake.nix";
  environment.etc."webtop/home-manager/home.nix".source = runtimeFile "home.nix";
  environment.etc."webtop/home-manager/packages.json".source = ../home-manager/packages.json;

  # `nix profile` with backends (ADR 0013).
  nix.package = nixpkgs.legacyPackages.${system}.callPackage ../nix { };

  # The generation and news file of the unchanged template for the placeholder account. A user with
  # another login name reuses their closure; only the few derivations carrying the name are built.
  system.extraDependencies = [ template.activationPackage template.config.news.json.output ];

  # Creates the starter flake once and keeps it on the current template and login name while it is
  # unmodified (image/webtop-home-manager-template.sh). When it writes, the setup unit below switches.
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
        runtimeInputs = with pkgs; [ coreutils diffutils gnugrep gnused templateTool ];
        text = builtins.readFile ../webtop-home-manager-template.sh;
      });
    };
  };

  # The notification for the above (title, then text), once Plasma (its notification server) is up.
  systemd.services.webtop-home-manager-notice = {
    description = "Tell the user what to do about their home-manager flake";
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
          webtop-notify "$(head -n1 ${notice})" \
            "$(tail -n +2 ${notice}) See: journalctl -u webtop-home-manager-template" dialog-warning
        '';
      });
    };
  };

  # Relock and switch (image/webtop-home-manager.sh), after the template unit. After an image
  # update the reinstall unit runs it instead, once the store paths are restored.
  systemd.services.webtop-home-manager = {
    description = "Bring the user's home-manager setup to the image's";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    # nix-daemon.service too: at shutdown the daemon must outlive a switch the stop waits for.
    after = [ "network-online.target" "nix-daemon.socket" "nix-daemon.service" "webtop-session.service" "webtop-home-manager-template.service" ];
    unitConfig.ConditionPathExists = "!/persist/nix/reinstall/pending";
    environment = {
      WEBTOP_FLAKE = "${webtopFlake}";
      DBUS_SESSION_BUS_ADDRESS = "unix:path=/run/user/${toString cfg.uid}/bus";
      XDG_RUNTIME_DIR = "/run/user/${toString cfg.uid}";
    };
    serviceConfig = {
      # Not oneshot: multi-user.target (and so the agent's ready report) would wait for a switch.
      Type = "exec";
      User = toString cfg.uid;
      ExecStart = lib.getExe setup;
      # The chart's terminationGracePeriodSeconds must cover this plus the session save.
      TimeoutStopSec = "5min";
    };
  };
}
