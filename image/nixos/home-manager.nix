# home-manager for the desktop user: the image's copy resolves from the registry, a starter flake is
# created on first boot, and its generation is prebuilt into the image. It also owns software
# installation (ADR 0011): the patched `nix profile` records packages in the flake's packages.json.
{ config, lib, pkgs, nixpkgs, home-manager, flake-registry, ... }:

let
  cfg = config.services.webtop;
  system = pkgs.stdenv.hostPlatform.system;
  home = config.users.users.${cfg.user}.home;
  vars = {
    user = cfg.user;
    inherit home;
    stateVersion = config.system.stateVersion;
  };
  # toFile, not replaceVars: home.nix is also imported below, which needs a plain path (no IFD).
  render = name: builtins.toFile name (lib.replaceStrings
    (map (n: "@${n}@") (lib.attrNames vars)) (lib.attrValues vars)
    (builtins.readFile ../home-manager/${name}));
  flakeFile = render "flake.nix";
  homeFile = render "home.nix";
  templateDir = pkgs.linkFarm "home-manager-template" {
    "flake.nix" = flakeFile;
    "home.nix" = homeFile;
    "packages.nix" = ../home-manager/packages.nix;
    "packages.json" = ../home-manager/packages.json;
  };
  pinned = [ "nixpkgs" "home-manager" ];
  template = home-manager.lib.homeManagerConfiguration {
    pkgs = nixpkgs.legacyPackages.${system};
    modules = [ homeFile ../home-manager/packages.nix ];
  };
  hmPackage = home-manager.packages.${system}.home-manager;

  # profile-hook (packages.nix): applies a changed packages.json.
  profileHook = pkgs.writeShellApplication {
    name = "webtop-profile-hook";
    runtimeInputs = [ pkgs.coreutils pkgs.git config.nix.package hmPackage ];
    text = builtins.readFile ../webtop-profile-hook.sh;
  };

  setup = pkgs.writeShellApplication {
    name = "webtop-home-manager";
    runtimeInputs = with pkgs; [ coreutils gnused gnugrep jq git libnotify config.nix.package hmPackage ];
    text = ''
      template_dir=${templateDir}
      template_marker=${home}/.local/state/webtop/home-manager-template
      known_flakes="
      7591b4f74ea2529ced8d8e492b1c754cff7ba27310a2d9e6448a6e94b1aae20b
      2ef7465ff1765e9e74543583a2a4481465fcc6554c838274c959cbc31612d0a9
      "
      ${builtins.readFile ../webtop-home-manager.sh}
    '';
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
  environment.systemPackages = [ hmPackage profileHook ];

  # `nix profile` with backends (ADR 0011).
  nix.package = nixpkgs.legacyPackages.${system}.callPackage ../nix { };

  # So `home-manager switch` of the unchanged template builds nothing: the generation and the news
  # file it also builds.
  system.extraDependencies = [ template.activationPackage template.config.news.json.output ];

  # Template, migration, relock and switch (image/webtop-home-manager.sh). After an image update the
  # reinstall unit runs it instead, once the store paths are restored.
  systemd.services.webtop-home-manager = {
    description = "Bring the user's home-manager setup to the image's";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [ "network-online.target" "nix-daemon.socket" "webtop-session.service" ];
    unitConfig.ConditionPathExists = "!/persist/nix/reinstall/pending";
    environment = {
      DBUS_SESSION_BUS_ADDRESS = "unix:path=/run/user/${toString cfg.uid}/bus";
      XDG_RUNTIME_DIR = "/run/user/${toString cfg.uid}";
    };
    serviceConfig = {
      # Not oneshot: multi-user.target (and so the agent's ready report) would wait for a switch.
      Type = "exec";
      User = cfg.user;
      ExecStart = lib.getExe setup;
      TimeoutStopSec = 10;
    };
  };
}
