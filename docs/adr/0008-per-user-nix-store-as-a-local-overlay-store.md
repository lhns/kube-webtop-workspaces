# 0008 — The user's Nix store is a local-overlay store over the image's store

- Status: Accepted (2026-10-29: the feasibility test passed; the image ran Plasma in a live pod with user namespaces, writable cgroups and the overlay store)
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

## Amendment 2026-10-28

Only the Nix daemon opens the overlay store; all clients, root included, go through it. The build
sandbox is off, because `pivot_root` fails in the user-namespaced pod.

## Amendment 2026-10-30

An image update keeps the old overlay until the users' live paths (GC roots, profiles, home-manager
generations) are copied into the new store after boot; paths only the old image had are substituted.
Nothing runs at shutdown; the reinstall remains the fallback.

## Amendment 2026-11-03

An image update keeps the overlay and invalidates only the upper paths whose closure the new image
breaks (with Nix's own GC, before the overlay is mounted); the whole-overlay reset of 2026-10-30 is
the fallback, entered from the exact pre-run state. The overlay is mounted with `index=off,xino=off`
and Nix runs with `fsync-store-paths = true`. A volume moved to an image of another architecture
shares no store paths with it, so everything is invalidated there.

## Amendment 2026-11-10

The overlay scripts, the image-update handling and the restore, reinstall and trash units moved to webtop-nix (its ADR 0013); see [0019](0019-desktop-features-that-need-no-kubernetes-come-from-webtop-nix.md). The decision above stands.
