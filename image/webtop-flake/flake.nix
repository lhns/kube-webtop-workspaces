# The image's own flake (registry entry `webtop`): modules for the user's home-manager flake.
{
  outputs = _: {
    # webtop.homeModules.packages ./packages.json
    homeModules.packages = import ./packages.nix;
  };
}
