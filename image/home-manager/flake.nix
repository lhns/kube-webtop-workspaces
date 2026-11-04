{
  inputs = {
    # nixpkgs, home-manager and webtop are the image's (its flake registry): no download; relocked after image updates.
    nixpkgs.url = "nixpkgs";
    home-manager = {
      url = "home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # The module behind packages.json: what `nix profile add` installs.
    webtop.url = "flake:webtop";
  };

  outputs = { nixpkgs, home-manager, webtop, ... }: {
    homeConfigurations."@user@" = home-manager.lib.homeManagerConfiguration {
      pkgs = nixpkgs.legacyPackages.@system@;
      # local.nix: your own module, never written by the image. In a git repo a flake sees only
      # tracked files, so `git add` it (as packages.json).
      modules = [
        ./home.nix
        (webtop.homeModules.packages ./packages.json)
      ] ++ nixpkgs.lib.optional (builtins.pathExists ./local.nix) ./local.nix;
    };
  };
}
