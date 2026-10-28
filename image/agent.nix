{ buildGoModule }:
buildGoModule {
  pname = "webtop-agent";
  version = "0.1.0";
  src = ../agent;
  # Update with agent/go.sum (the CI nix job fails on a stale hash).
  vendorHash = "sha256-y+3dL8WEiN4PDHLX9/iNijS20wUheiJ+mZrM7af69G0=";
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" ];
  subPackages = [ "cmd/webtop-agent" ];
  meta.mainProgram = "webtop-agent";
}
