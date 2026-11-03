# 0015 — Desktop image layered by webtop-nix's builder

- Status: Accepted
- Date: 2026-11-03

## Context

The desktop image (ADR 0007) is several GB. nix2container's popularity layering put almost all of
it, the Nix DB included, in one layer that changed on every build, so every update was a full pull
on every node. webtop-nix now has an image builder that splits a closure into stable layers and
publishes its own standalone image (webtop-nix ADR 0005).

## Decision

- The desktop image is built with webtop-nix's `lib.buildImage` from this system's own closure,
  with kww's own groups on top of webtop-nix's (the flake sources baked into the registry, then kww's
  packages and config). No `FROM` and no layers shared with the webtop-nix image: each image's
  layers hold only its own paths, so the store stays this system's closure and the image stamp
  (ADR 0008) keeps covering it.
- Layers are pushed zstd-compressed at webtop-nix's level with its pinned skopeo.
- webtop-nix owns the nixpkgs pin; this flake follows it (`inputs.nixpkgs.follows`), which keeps
  one Qt tree with the grafted KWin and one skopeo.

## Consequences

- A kww or webtop-nix change that leaves nixpkgs alone pulls only the changed layers; a nixpkgs
  bump is still a full pull.
- nixpkgs moves by bumping the webtop-nix pin; `nixpkgs-sync` checks the follows.
- Changing webtop-nix's group anchors or order, the skopeo, or the compression level changes every
  layer digest once.
