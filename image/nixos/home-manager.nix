# Software installation (webtop-nix's services.webtop.nixProfile, its ADR 0014), fitted to the pod: the home
# on the volume, and the agent's view of the restore's progress. The store restore and reinstall after an
# image update come with storeOverlay (container.nix).
{ ... }:

{
  services.webtop.nixProfile = {
    enable = true;
    # The home on the volume, whatever paths it is mounted at (webtop-init). Existing homes'
    # templates.sha256 are keyed by this: changing it makes every unmodified starter flake look edited.
    homeStoragePath = "/persist/home/user";
    # WEBTOP_HOME_PATHS: the paths the home was mounted at, so earlier-rendered starters stay unmodified.
    templateEnvironmentFiles = [ "-/run/webtop/env" ];
  };
}
