# The desktop itself (webtop-nix's services.webtop), wired to the pod contract (docs/contract.md).
{ ... }:

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

  # webtop-init writes SELKIES_PORT=$WEBTOP_HTTP_PORT here; EnvironmentFile wins over Environment.
  systemd.user.services.selkies.serviceConfig.EnvironmentFile = "-/run/webtop/selkies.env";
}
