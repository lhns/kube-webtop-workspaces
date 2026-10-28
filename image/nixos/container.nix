# NixOS as a container: systemd PID 1 behind webtop-init, kubelet-managed network files, the
# per-user local-overlay Nix store, and the journal on the container log.
{ config, lib, pkgs, ... }:

let
  # Lower: the image's /nix, bound read-only at /.lower by webtop-init. Upper and DB: the volume.
  overlayStore = "local-overlay://?lower-store=local%3Froot%3D%2F.lower%26read-only%3Dtrue"
    + "&upper-layer=/persist/nix/upper&check-mount=true&ignore-gc-delete-failure=true"
    + "&remount-hook=${remountHook}";
  # Called by nix after deleting upper paths (GC), so overlayfs drops its cached lookups.
  remountHook = pkgs.writeShellScript "webtop-remount-store" ''
    exec ${pkgs.util-linux}/bin/mount -o remount /nix/store
  '';
in
{
  boot.isContainer = true;
  system.stateVersion = "26.05";

  # kubelet/runtime manage these; NixOS must not touch them.
  networking.hostName = "";
  networking.useDHCP = false;
  networking.useHostResolvConf = false;
  networking.resolvconf.enable = false;
  services.resolved.enable = false;
  environment.etc."resolv.conf".enable = false;
  environment.etc."hosts".enable = false;
  networking.firewall.enable = false;

  # kubelet mounts these; activation's remount of them fails harmlessly but noisily.
  boot.specialFileSystems."/dev/shm".enable = false;
  boot.specialFileSystems."/run".enable = false;

  # Only the daemon opens the overlay store; every client (root included) goes through it. A client
  # with the overlay URL would need write access, and root's default "auto" store would see only
  # the upper DB.
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" "local-overlay-store" "read-only-local-store" ];
    store = "daemon";
    # The build sandbox fails in the pod (pivot_root: EPERM under the user namespace).
    sandbox = false;
    trusted-users = [ "root" config.services.webtop.user ];
  };
  # %% : systemd would read the URL's %-escapes as unit specifiers.
  systemd.services.nix-daemon.environment.NIX_CONFIG =
    "store = ${lib.replaceStrings [ "%" ] [ "%%" ] overlayStore}";
  nix.checkConfig = false; # would try to open the store at build time

  # Journal to the container log (kubectl logs): systemd replaces PID 1's stdout, and journald's
  # ForwardToConsole needs /dev/console (tty: true). webtop-init relays this fifo to the real stdout.
  systemd.services.webtop-journal-relay = {
    description = "Relay the journal to the container log";
    wantedBy = [ "sysinit.target" ];
    after = [ "systemd-journald.service" ];
    unitConfig.DefaultDependencies = false;
    serviceConfig = {
      Restart = "always";
      ExecStart = "${pkgs.runtimeShell} -c 'exec ${pkgs.systemd}/bin/journalctl -b -f -n all -o short-precise > /run/webtop/console'";
    };
  };

  # No virtual consoles in a container (they restart in a loop).
  systemd.services."getty@tty1".enable = false;
  systemd.services."autovt@".enable = false;
  systemd.services."serial-getty@".enable = false;
  systemd.services."console-getty".enable = false;
  systemd.services.systemd-oomd.enable = false;
  # rtkit-daemon fails (cap_set_proc EPERM) and degrades the system; PipeWire runs without it.
  security.rtkit.enable = lib.mkForce false;
  # Needs /dev/fuse, which a pod does not have; the unit would fail and degrade the user manager.
  systemd.user.units."xdg-document-portal.service".enable = false;
  # getty.target pulls in getty@tty1 from systemd's own units.
  systemd.suppressedSystemUnits = [ "getty@.service" "autovt@.service" ];
  services.logind.settings.Login = { NAutoVTs = 0; ReserveVT = 0; };

  # The desktop user owns the container (it is user-namespaced); sudo without a password.
  users.users.${config.services.webtop.user} = {
    extraGroups = [ "wheel" ];
    initialHashedPassword = "";
  };
  security.sudo.wheelNeedsPassword = false;

  environment.systemPackages = with pkgs; [
    util-linux procps iproute2 findutils gnugrep file lsof
    git jq curl wget htop home-manager libnotify
  ];
}
