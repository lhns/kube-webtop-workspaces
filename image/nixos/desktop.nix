# The desktop itself (webtop-nix's services.webtop), wired to the pod contract (docs/contract.md).
{ ... }:

{
  services.webtop = {
    enable = true;
    # A placeholder: renamed at activation to the user's login name (identity.nix). uid 1000 never
    # changes, so the volume's file ownership does not either.
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

  services.webtop.softwareCenter.enable = true;

  # With no crashes to hand off this unit only watches the journal, and its 30-minute limit then counts as a
  # failure: every session goes degraded half an hour after login.
  systemd.user.services.drkonqi-coredump-pickup = {
    overrideStrategy = "asDropin";
    serviceConfig.RuntimeMaxSec = "infinity";
  };
  # The session (Plasma and its apps) gets webtop-init's session.env, e.g. the software center's default.
  systemd.services.webtop-session.serviceConfig.EnvironmentFile = [ "-/run/webtop/session.env" ];

  # webtop-init writes SELKIES_PORT and the cluster's desktop.selkies / desktop.title here
  # (docs/contract.md, "Selkies settings"); it wins over extraEnvironment above, and the user's
  # allowlisted ~/.config/webtop/selkies.env (services.webtop.userSettings) wins over it.
  services.webtop.environmentFiles = [ "-/run/webtop/selkies.env" ];
}
