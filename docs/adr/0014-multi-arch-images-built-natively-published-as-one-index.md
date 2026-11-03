# 0014 — Multi-arch images, built natively, published as one index

- Status: Accepted
- Date: 2026-11-02

## Context

The desktop and controller images were amd64 only (the controller added arm64 on release tags).
Nothing in the stack needs x86: pixelflux and pcmflux build from source, Selkies is a pure-Python
wheel, KWin and llvmpipe build on aarch64. The desktop system cannot be cross-evaluated: webtop-nix
grafts its patched KWin by reading the stock closure (import from derivation), so evaluating the
aarch64 system builds aarch64 derivations. Deployments pin images as `tag@digest`.

## Decision

- Supported systems: `x86_64-linux` and `aarch64-linux`, listed once in `flake.nix`.
- The desktop image is built and boot-tested **natively** per architecture (GitHub's arm runners),
  never under emulation. Each per-arch image is pushed **by digest only**; one job then creates an
  **OCI index** carrying every tag. A tag's digest is the index digest.
- The controller image is a cross-compiled amd64+arm64 index on every publish, dev builds included.
- cosign signs the index and each platform manifest (`--recursive`); SBOMs are per architecture.

## Consequences

- Pins stay one line (`tag@<index digest>`); the kubelet picks the node's platform.
- CI runs the desktop build and boot tests twice; a cold KWin build happens once per architecture.
- Per-arch manifests are untagged. `prune-ghcr.sh` keeps untagged versions, so those of pruned
  indexes, and their `sha256-<digest>.sig`/`.att` tags, accumulate until the script learns to follow
  index references.
- A desktop's persisted state is per architecture: the user's Nix store overlay holds binaries for
  one, and the starter home-manager flake names its system. In a mixed-arch cluster, pin desktops to
  one architecture (`desktop.nodeSelector` `kubernetes.io/arch`).
