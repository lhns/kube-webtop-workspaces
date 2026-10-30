# home-manager for the desktop user: the image's copy resolves from the registry, a starter flake is
# created on first boot, and its generation is prebuilt into the image.
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
  dir = "${home}/.config/home-manager";
  marker = "${home}/.local/state/webtop/home-manager-template";
  pinned = [ "nixpkgs" "home-manager" ];
  template = home-manager.lib.homeManagerConfiguration {
    pkgs = nixpkgs.legacyPackages.${system};
    modules = [ homeFile ];
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
  environment.systemPackages = [ home-manager.packages.${system}.home-manager ];

  # So `home-manager switch` of the unchanged template builds nothing: the generation and the news
  # file it also builds.
  system.extraDependencies = [ template.activationPackage template.config.news.json.output ];

  # Once: the marker keeps the template from coming back after the user deletes it.
  systemd.services.webtop-home-manager-template = {
    description = "Create the starter home-manager flake";
    wantedBy = [ "multi-user.target" ];
    before = [ "webtop-session.service" ];
    unitConfig.ConditionPathExists = [ "!${dir}" "!${marker}" ];
    serviceConfig = {
      Type = "oneshot";
      User = cfg.user;
      ExecStart = pkgs.writeShellScript "webtop-home-manager-template" ''
        set -e
        export PATH=${lib.makeBinPath [ pkgs.coreutils ]}
        mkdir -p ${dir} ${builtins.dirOf marker}
        cp ${flakeFile} ${dir}/flake.nix
        cp ${homeFile} ${dir}/home.nix
        chmod u+w ${dir}/flake.nix ${dir}/home.nix
        touch ${marker}
      '';
    };
  };
}
