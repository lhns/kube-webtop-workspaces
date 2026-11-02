{ pkgs, ... }:

{
  home.username = "@user@";
  home.homeDirectory = "@home@";
  home.stateVersion = "@stateVersion@";

  # Packages: `nix profile add nixpkgs#hello` records them in packages.json and switches; `nix shell nixpkgs#hello` tries one without installing.
  # Packages listed here work too; run `home-manager switch` after editing.
  home.packages = with pkgs; [
    # hello
  ];

  programs.home-manager.enable = true;
}
