{
  description = "kube-webtop-workspaces desktop image: NixOS (systemd PID 1) with a browser-streamed Plasma desktop";

  inputs = {
    # webtop-nix's nixpkgs: one Qt tree with its grafted KWin (README, "KWin patch"), and its image
    # builder's skopeo, so a layer compresses to the same blob as in the last build.
    nixpkgs.follows = "webtop-nix/nixpkgs";
    # The users' home-manager flakes resolve it from the system registry (image/nixos/home-manager.nix).
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Upstream global flake registry, baked in with the image's own pins (image/nixos/home-manager.nix).
    flake-registry = {
      url = "github:NixOS/flake-registry";
      flake = false;
    };
    webtop-nix.url = "github:lhns/webtop-nix/e46715e063dec2a9a6968b205751ffa3540d3626";
  };

  outputs = { self, nixpkgs, home-manager, flake-registry, webtop-nix, ... }:
    let
      # Supported systems (ADR 0014); CI builds and boot-tests each one natively, in a matrix.
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;

      perSystem = system: rec {
        pkgs = nixpkgs.legacyPackages.${system};

        agent = pkgs.callPackage ./image/agent.nix { };

        nixos = nixpkgs.lib.nixosSystem {
          inherit system;
          specialArgs = { inherit agent nixpkgs home-manager flake-registry; };
          modules = [
            webtop-nix.nixosModules.webtop
            ./image/nixos/container.nix
            ./image/nixos/desktop.nix
            ./image/nixos/hooks.nix
            ./image/nixos/home-manager.nix
            ./image/nixos/identity.nix
          ];
        };
        toplevel = nixos.config.system.build.toplevel;

        # Login names webtop-init refuses (ADR 0011): every users.users and users.groups name, i.e. what
        # activation writes to the image's /etc/passwd and /etc/group (not there yet when webtop-init
        # runs), except the desktop user's placeholder, which is renamed.
        reservedNames = let c = nixos.config; in pkgs.writeText "webtop-reserved-names" (pkgs.lib.concatLines
          (pkgs.lib.remove c.services.webtop.user (pkgs.lib.unique
            (pkgs.lib.mapAttrsToList (_: u: u.name) c.users.users ++ pkgs.lib.mapAttrsToList (_: g: g.name) c.users.groups))));

        # The shebangs are a static busybox: they run before /nix/store is overlaid. nix-store runs from
        # the image's store, which is what /nix/store is until then.
        webtopInit = pkgs.runCommand "webtop-init" { } ''
          mkdir -p $out
          substitute ${./image/webtop-init} $out/webtop-init \
            --subst-var-by busybox ${pkgs.pkgsStatic.busybox} \
            --subst-var-by reservedNames ${reservedNames}
          substitute ${./image/webtop-nix-lower} $out/webtop-nix-lower \
            --subst-var-by busybox ${pkgs.pkgsStatic.busybox}
          substitute ${./image/webtop-nix-partial} $out/webtop-nix-partial \
            --subst-var-by busybox ${pkgs.pkgsStatic.busybox} \
            --subst-var-by nixStore ${nixos.config.nix.package}/bin/nix-store
          chmod +x $out/webtop-init $out/webtop-nix-lower $out/webtop-nix-partial
        '';

        rootfs = pkgs.runCommand "rootfs" { } ''
          mkdir -p $out/sbin $out/bin $out/etc $out/run $out/tmp $out/var $out/persist $out/home $out/proc $out/sys $out/dev
          chmod 1777 $out/tmp
          ln -s ${toplevel}/init $out/sbin/init
          ln -s ${pkgs.pkgsStatic.busybox}/bin/busybox $out/bin/busybox
          ln -s ${pkgs.pkgsStatic.busybox}/bin/sh $out/bin/sh
        '';

        # Identifies the image's store (the overlay's lower). webtop-nix-lower compares it with the stamp
        # on the volume; it lives outside /nix so busybox can read it before the overlay is mounted.
        # Over paths and NAR hashes: a path rebuilt with other bytes is a change too. The image's
        # store is exactly this closure: its layers may hold only the toplevel's.
        lowerClosure = pkgs.closureInfo { rootPaths = [ toplevel webtopInit rootfs ]; };
        stamp = pkgs.runCommand "webtop-image-stamp" { } ''
          mkdir -p $out
          sha256sum ${lowerClosure}/registration | cut -d' ' -f1 > $out/webtop-image-stamp
        '';
      };
      sys = forAllSystems perSystem;
    in
    {
      nixosConfigurations = nixpkgs.lib.mapAttrs' (system: s: nixpkgs.lib.nameValuePair "desktop-${system}" s.nixos) sys;

      packages = forAllSystems (system: with sys.${system}; {
        inherit toplevel agent;
        # The patched KWin the system grafts in; CI builds and caches it ahead of the image.
        kwin = nixos.pkgs.kwinSoftwarePresentation;
        # webtop-nix's builder (its ADR 0005): layers split from this system's closure, stable to
        # volatile, the Nix DB and copyToRoot on top.
        image = webtop-nix.lib.buildImage {
          name = "ghcr.io/lhns/kube-webtop-workspaces-desktop";
          tag = "dev";
          inherit nixos;
          # Change with input bumps only: the sources baked into the registry (home-manager.nix), and
          # the all-locales glibc the prebuilt home-manager generation references.
          extraGroups = [
            { name = "sources"; anchors = [ nixpkgs home-manager ]; }
            { name = "locales"; anchors = [ pkgs.glibcLocales ]; }
          ];
          copyToRoot = [ rootfs webtopInit stamp ];
          config = {
            Entrypoint = [ "/webtop-init" ];
            StopSignal = "SIGRTMIN+3";
            # PATH for `kubectl exec`; webtop-init sets its own.
            Env = [ "container=docker" "PATH=/run/current-system/sw/bin:/run/wrappers/bin:/bin" ];
          };
        };
        default = self.packages.${system}.image;
        inherit (webtop-nix.packages.${system}) layer-report;
      });

      checks = forAllSystems (system: with sys.${system}; {
        inherit agent;
        # Evaluates the whole system without building it.
        toplevel-eval = pkgs.writeText "toplevel-eval" (builtins.unsafeDiscardStringContext toplevel.drvPath);
      });
    };
}
