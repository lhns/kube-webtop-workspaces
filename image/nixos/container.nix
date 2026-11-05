# NixOS as a container in a pod: webtop-nix's container module (runtime-owned files, no consoles,
# the local-overlay Nix store, the journal on the container log) fitted to kubelet and webtop-init.
{ ... }:

{
  system.stateVersion = "26.05";

  services.webtop = {
    storeOverlay.enable = true; # webtop-init mounts the overlay and binds the lower store at /.lower
    journalRelay.enable = true; # webtop-init relays the fifo (default path) to the real stdout
    adminTools.enable = true;
    # The desktop user owns the container (it is user-namespaced); sudo without a password.
    sudo.enable = true;
  };

  # kubelet mounts these; activation's remount of them fails harmlessly but noisily.
  boot.specialFileSystems."/dev/shm".enable = false;
  boot.specialFileSystems."/run".enable = false;

  nix.settings = {
    # By group: the desktop user's name is chosen at runtime (identity.nix).
    trusted-users = [ "root" "@wheel" ];
    # A registered path's bytes are on disk before its row: the image-update invalidation
    # (webtop-nix-partial) keeps upper paths by their rows. Costs an fsync per installed path.
    fsync-store-paths = true;
  };
  # Extra substituters/keys from the chart (desktop.nix.*), written by webtop-init; optional include.
  nix.extraOptions = "!include /run/webtop/nix.conf";
}
