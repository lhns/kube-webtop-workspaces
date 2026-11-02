# The desktop itself (webtop-nix's services.webtop), wired to the pod contract (docs/contract.md).
{ pkgs, ... }:

{
  services.webtop = {
    enable = true;
    user = "user";
    uid = 1000;
    # The gateway proxies to the pod; the NetworkPolicy admits only the gateway.
    address = "0.0.0.0";
    port = 8080; # default; WEBTOP_HTTP_PORT overrides it at runtime (below)
    extraEnvironment = {
      # TLS ends at the ingress; the gateway speaks plain HTTP to the pod (contract section 5).
      SELKIES_ENABLE_HTTPS = "false";
      XCURSOR_THEME = "breeze_cursors";
      XCURSOR_SIZE = "24";
      # Start at 30 fps: software x264 on shared CPUs keeps up; the client may still raise it.
      SELKIES_FRAMERATE = "30,8-240";
    };
  };

  # One way to install software (ADR 0013): Discover has no Nix backend on NixOS. Plasma's "Get New…"
  # dialogs (KNewStuff) stay.
  environment.plasma6.excludePackages = [ pkgs.kdePackages.discover ];

  # webtop-init writes SELKIES_PORT and the cluster's desktop.selkies / desktop.title here
  # (docs/contract.md, "Selkies settings"); it wins over extraEnvironment above, and the user's
  # allowlisted ~/.config/webtop/selkies.env (services.webtop.userSettings) wins over it.
  services.webtop.environmentFiles = [ "-/run/webtop/selkies.env" ];
}
