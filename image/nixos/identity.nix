# The desktop user's identity (ADR 0011): the account is declared as the placeholder
# `services.webtop.user` (uid 1000) and renamed at activation to the login name webtop-init chose,
# with home /home/<name> and the display name as GECOS. Everything else refers to uid 1000.
{ config, lib, pkgs, ... }:

let
  cfg = config.services.webtop;
in
{
  # The rename edits what update-users-groups.pl wrote; the other user managers regenerate passwd
  # differently.
  assertions = [{
    assertion = !config.systemd.sysusers.enable && !config.services.userborn.enable;
    message = "image/nixos/identity.nix renames the desktop user after update-users-groups.pl";
  }];

  # webtop-init creates /persist/home/user and mounts it at the home paths; a created home would be
  # a stray /home/user in the /home tmpfs.
  users.users.${cfg.user}.createHome = lib.mkForce false;

  # Activation runs once per container (its rootfs, /etc included, is fresh every start), so the
  # rename holds until the container stops. usermod also renames the group memberships. The
  # activation PATH has coreutils, grep, shadow and util-linux, not sed.
  system.activationScripts.webtop-identity = lib.stringAfter [ "users" "groups" "etc" ] ''
    # Read, not sourced: the GECOS comes from LDAP.
    name=$(grep -m1 '^WEBTOP_LOGIN_NAME=' /run/webtop/env | cut -d= -f2-)
    gecos=$(grep -m1 '^WEBTOP_LOGIN_GECOS=' /run/webtop/env | cut -d= -f2-)
    rm -f /run/webtop/identity-error
    if [ -n "$name" ] && [ "$name" != ${cfg.user} ] && ! reason=$(usermod -l "$name" ${cfg.user} 2>&1); then
      # Stay `user` with home /home/user, consistently; webtop-init mounted only /home/<name>.
      # The agent reports the reason (condition LoginName, RenameFailed).
      echo "webtop-identity: renaming ${cfg.user} to $name failed; staying ${cfg.user}: $reason"
      printf '%s' "${reason:-usermod failed}" > /run/webtop/identity-error
      name=${cfg.user}
      mountpoint -q /home/${cfg.user} || { mkdir -p /home/${cfg.user} && mount --bind /persist/home/user /home/${cfg.user}; }
    fi
    if [ -n "$name" ]; then
      usermod -d "/home/$name" -c "''${gecos:-$name}" "$name" \
        || echo "webtop-identity: setting the home and GECOS of $name failed"
      # usermod does not rename the subordinate id ranges. Lingering goes by uid (webtop-nix).
      for f in /etc/subuid /etc/subgid; do
        [ ! -f "$f" ] || ${pkgs.gnused}/bin/sed -i "s/^${cfg.user}:/$name:/" "$f"
      done
    fi
  '';
}
