# Installs what packages.json declares and points `nix profile` at that file, so `nix profile add`,
# `remove`, `upgrade` and `rollback` edit it and switch. Without this module, `nix profile` is stock.
{ config, lib, pkgs, ... }:

let
  system = pkgs.stdenv.hostPlatform.system;
  attrPath = e: lib.splitString "." e.attrPath;
  # flake:nixpkgs entries use this configuration's nixpkgs (the image's); others their locked flake.
  fromNixpkgs = e:
    let p = attrPath e;
    in lib.getAttrFromPath (if lib.length p > 2 && lib.elemAt p 1 == system then lib.drop 2 p else p) pkgs;
  package = e:
    let
      drv = if (e.originalUrl or "") == "flake:nixpkgs" then fromNixpkgs e
        else lib.getAttrFromPath (attrPath e) (builtins.getFlake e.url);
      outputs = e.outputs or null;
      selected = if outputs == null then [ drv ]
        else map (o: drv.${o}) (if lib.elem "*" outputs then drv.outputs else outputs);
    in map (lib.setPrio (e.priority or 5)) selected;
in
{
  home.packages = lib.concatMap package (lib.attrValues (lib.importJSON ./packages.json).elements);

  # Not nix.settings: home-manager validates those with nix.package, which would have to be the
  # image's patched Nix. Moving them there needs that, or the file conflicts with nix.settings.
  xdg.configFile."nix/nix.conf".text = ''
    profile-backend = declaration:${config.xdg.configHome}/home-manager/packages.json
    profile-hook = /run/current-system/sw/bin/webtop-profile-hook
  '';
}
