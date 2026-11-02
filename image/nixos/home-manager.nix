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
  environment.systemPackages = [ home-manager.packages.${system}.home-manager ];

  # The generation and news file of the unchanged template for the placeholder account. A user with
  # another login name reuses their closure; only the few derivations carrying the name are built.
  system.extraDependencies = [ template.activationPackage template.config.news.json.output ];

  # Once: the marker keeps the template from coming back after the user deletes it.
  systemd.services.webtop-home-manager-template = {
    description = "Create the starter home-manager flake";
    wantedBy = [ "multi-user.target" ];
    before = [ "webtop-session.service" ];
    unitConfig.ConditionPathExists = [ "!${dir}" "!${marker}" ];
    serviceConfig = {
      Type = "oneshot";
      User = toString cfg.uid;
      # USER and HOME come from passwd: the login name and /home/<name>.
      ExecStart = pkgs.writeShellScript "webtop-home-manager-template" ''
        set -e
        export PATH=${lib.makeBinPath [ pkgs.coreutils pkgs.gnused ]}
        mkdir -p ${dir} ${builtins.dirOf marker}
        sed -e "s|@user@|$USER|g" -e "s|@home@|$HOME|g" ${runtimeFile "flake.nix"} > ${dir}/flake.nix
        sed -e "s|@user@|$USER|g" -e "s|@home@|$HOME|g" ${runtimeFile "home.nix"} > ${dir}/home.nix
        touch ${marker}
      '';
    };
  };
}
