# Software installation (webtop-nix's services.webtop.nixProfile, ADR 0013), fitted to the pod: the home
# on the volume, the store restore after an image update, and the agent's view of its progress.
{ ... }:

{
  services.webtop.nixProfile = {
    enable = true;
    # The home on the volume, whatever paths it is mounted at (webtop-init). Existing homes'
    # templates.sha256 are keyed by this: changing it makes every unmodified starter flake look edited.
    homeStoragePath = "/persist/home/user";
    # The home's store survives an image update here: webtop-nix-restore, then the reinstall unit (hooks.nix).
    fromImageStore = false;
  };

  systemd.services.webtop-home-manager-template = {
    before = [ "webtop-nix-reinstall.service" ];
    serviceConfig.EnvironmentFile = "-/run/webtop/env"; # WEBTOP_HOME_PATHS
  };

  # After an image update the reinstall unit runs this instead, once the store paths are restored.
  # The chart's terminationGracePeriodSeconds must cover its TimeoutStopSec plus the session save.
  systemd.services.webtop-home-manager.unitConfig.ConditionPathExists = "!/persist/nix/reinstall/pending";
}
