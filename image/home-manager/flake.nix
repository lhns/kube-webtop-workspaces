{
  inputs = {
    # nixpkgs and home-manager are the image's (its flake registry): no download; relocked after image updates.
    nixpkgs.url = "nixpkgs";
    home-manager = {
      url = "home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { nixpkgs, home-manager, ... }: {
    homeConfigurations."@user@" = home-manager.lib.homeManagerConfiguration {
      pkgs = nixpkgs.legacyPackages.@system@;
      # packages.nix: what `nix profile add` installs (packages.json).
      modules = [ ./home.nix ./packages.nix ];
    };
  };
}
