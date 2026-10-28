# 0008 — The user's Nix store is a local-overlay store over the image's store

- Status: Proposed (pending the feasibility test)
- Date: 2026-10-28

## Context

Users install software with Nix (home-manager, `nix profile`), and it must persist on their PVC.
The image already carries a large store; copying it into every PVC wastes space and time, and a
store at another path than `/nix/store` cannot use binary caches.

## Decision

- `/nix/store` is an overlayfs: the image's store as the read-only lower layer, the PVC as the
  upper layer. Nix uses it as a **`local-overlay` store** (experimental feature), with the overlay
  store's own database, GC roots and profiles persisted on the PVC.
- A small entrypoint sets up the mounts before it executes systemd, since systemd itself runs from
  `/nix/store`.
- The overlay store requires the lower layer never to lose paths, which an image update does. On
  an image change the store is treated as a cache: the user's profile entries are recorded, the
  upper layer is reset, and home-manager and the profile are rebuilt in the background. Escrowing
  the old paths before the update, to keep locally built ones, replaces the reset later.

## Consequences

- No per-user copy of the system; the user's own paths cost only what they add.
- The user's declared software (their home-manager flake) is the source of truth; imperatively
  installed paths are rebuilt after image updates.
- Depends on an experimental Nix feature.
