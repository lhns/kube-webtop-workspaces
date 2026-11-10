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
    };
  };

  services.webtop.softwareCenter.enable = true;

  # The session (Plasma and its apps) gets webtop-init's session.env, e.g. the software center's default.
  services.webtop.sessionEnvironmentFiles = [ "-/run/webtop/session.env" ];

  # Keys of ~/.config/webtop/session.conf that the agent reads (webtop.ssoLogout writes them).
  services.webtop.sessionConfKeys = [ "onLogout" "onLock" ];

  # webtop-init writes SELKIES_PORT and the cluster's desktop.selkies / desktop.title here
  # (docs/contract.md, "Selkies settings"); it wins over extraEnvironment above, and the user's
  # allowlisted ~/.config/webtop/selkies.env (services.webtop.userSettings) wins over it.
  services.webtop.environmentFiles = [ "-/run/webtop/selkies.env" ];

  # WEBTOP_TIME_ZONE / WEBTOP_LOCALE (docs/contract.md, "Time zone and locale") reach the session through
  # the env webtop-init copies here.
  services.webtop.localeEnvironmentFiles = [ "-/run/webtop/env" ];

  # WEBTOP_LOCK_SCREEN (the chart's gateway.lockLogsOut) shows Lock in Plasma; read at each session start.
  services.webtop.lockEnvironmentFiles = [ "-/run/webtop/env" ];
}
