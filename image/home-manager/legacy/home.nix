{ pkgs, ... }:

{
  home.username = "@user@";
  home.homeDirectory = "@home@";
  home.stateVersion = "@stateVersion@";

  # Add a package, then run `home-manager switch`.
  home.packages = with pkgs; [
    # hello
  ];

  programs.home-manager.enable = true;
}
