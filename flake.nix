{
  description = "kube-webtop-workspaces desktop image: NixOS (systemd PID 1) with a browser-streamed Plasma desktop";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nix2container = {
      url = "github:nlewo/nix2container";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    webtop-nix = {
      url = "github:lhns/webtop-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nix2container, webtop-nix, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      n2c = nix2container.packages.${system}.nix2container;

      agent = pkgs.callPackage ./image/agent.nix { };

      nixos = nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = { inherit agent; };
        modules = [
          webtop-nix.nixosModules.webtop
          ./image/nixos/container.nix
          ./image/nixos/desktop.nix
          ./image/nixos/hooks.nix
        ];
      };
      toplevel = nixos.config.system.build.toplevel;

      # The shebang is a static busybox: it runs before /nix/store is overlaid.
      webtopInit = pkgs.runCommand "webtop-init" { } ''
        mkdir -p $out
        substitute ${./image/webtop-init} $out/webtop-init \
          --subst-var-by busybox ${pkgs.pkgsStatic.busybox}
        chmod +x $out/webtop-init
      '';

      rootfs = pkgs.runCommand "rootfs" { } ''
        mkdir -p $out/sbin $out/bin $out/etc $out/run $out/tmp $out/var $out/persist $out/home $out/proc $out/sys $out/dev
        chmod 1777 $out/tmp
        ln -s ${toplevel}/init $out/sbin/init
        ln -s ${pkgs.pkgsStatic.busybox}/bin/busybox $out/bin/busybox
        ln -s ${pkgs.pkgsStatic.busybox}/bin/sh $out/bin/sh
      '';

      # Identifies the image's store (the overlay's lower). webtop-init compares it with the stamp on
      # the volume; it lives outside /nix so busybox can read it before the overlay is mounted.
      lowerClosure = pkgs.closureInfo { rootPaths = [ toplevel webtopInit rootfs ]; };
      stamp = pkgs.runCommand "webtop-image-stamp" { } ''
        mkdir -p $out
        sha256sum ${lowerClosure}/store-paths | cut -d' ' -f1 > $out/webtop-image-stamp
      '';
    in
    {
      nixosConfigurations.desktop = nixos;

      packages.${system} = {
        inherit toplevel agent;
        image = n2c.buildImage {
          name = "ghcr.io/lhns/kube-webtop-workspaces-desktop";
          tag = "dev";
          initializeNixDatabase = true;
          maxLayers = 100;
          copyToRoot = [ rootfs webtopInit stamp ];
          config = {
            Entrypoint = [ "/webtop-init" ];
            StopSignal = "SIGRTMIN+3";
            Env = [ "container=docker" ];
          };
        };
        default = self.packages.${system}.image;
      };

      checks.${system} = {
        inherit agent;
        # Evaluates the whole system without building it.
        toplevel-eval = pkgs.writeText "toplevel-eval" (builtins.unsafeDiscardStringContext toplevel.drvPath);
      };
    };
}
