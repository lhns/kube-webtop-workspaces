# The desktop itself (webtop-nix's services.webtop), wired to the pod contract (docs/contract.md).
{ lib, pkgs, ... }:

let
  # pixelflux dlopens these by soname (libwayland-server.so even unversioned), which its RUNPATH
  # does not cover: without them its compositor fails ("could not load libwayland-server.so").
  dlopenLibs = with pkgs; [
    wayland libglvnd mesa libdrm libva vulkan-loader libxkbcommon pixman libx11 libxcb
  ];
  selkies = pkgs.symlinkJoin {
    name = "selkies-wrapped-${pkgs.selkies.version}";
    paths = [ pkgs.selkies ];
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postBuild = ''
      wrapProgram $out/bin/selkies --prefix LD_LIBRARY_PATH : ${lib.makeLibraryPath dlopenLibs}
    '';
    meta.mainProgram = "selkies";
  };
in
{
  services.webtop = {
    enable = true;
    user = "user";
    uid = 1000;
    package = selkies;
    # The gateway proxies to the pod; the NetworkPolicy admits only the gateway.
    address = "0.0.0.0";
    port = 8080; # default; WEBTOP_HTTP_PORT overrides it at runtime (below)
    extraEnvironment = {
      # TLS ends at the ingress; the gateway speaks plain HTTP to the pod (contract section 5).
      SELKIES_ENABLE_HTTPS = "false";
      # The monitor of the null sink below.
      SELKIES_AUDIO_DEVICE_NAME = "output.monitor";
      # pixelflux's cursor loader reads only XCURSOR_PATH (else a placeholder cursor).
      XCURSOR_PATH = "/run/current-system/sw/share/icons:/run/current-system/sw/share/pixmaps";
      XCURSOR_THEME = "breeze_cursors";
      XCURSOR_SIZE = "24";
    };
  };

  systemd.services.webtop-session.environment = {
    # seat0 has no VTs in a container, so logind rejects a session on it (CreateSession:
    # InvalidParameter); without a logind session there is no session bus and Plasma does not start.
    XDG_SEAT = lib.mkForce "";
    # No GPU: Mesa would try zink/dri2 first and fail; go straight to llvmpipe.
    LIBGL_ALWAYS_SOFTWARE = "1";
  };

  # webtop-init writes SELKIES_PORT=$WEBTOP_HTTP_PORT here; EnvironmentFile wins over Environment.
  systemd.user.services.selkies.serviceConfig.EnvironmentFile = "-/run/webtop/selkies.env";

  # A pod has no sound card: a virtual sink named "output" for apps, whose monitor Selkies streams.
  services.pipewire.extraConfig.pipewire."90-webtop-output" = {
    "context.objects" = [{
      factory = "adapter";
      args = {
        "factory.name" = "support.null-audio-sink";
        "node.name" = "output";
        "node.description" = "Browser audio";
        "media.class" = "Audio/Sink";
        "audio.position" = "FL,FR";
        "priority.session" = 2000; # default sink
      };
    }];
  };
}
