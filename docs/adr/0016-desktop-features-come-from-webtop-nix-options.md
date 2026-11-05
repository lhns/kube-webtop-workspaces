# 0016 — Desktop features that need no Kubernetes come from webtop-nix options

- Status: Accepted
- Date: 2026-11-05

## Context

webtop-nix (its ADR 0006, 0008) holds what a single desktop needs without Kubernetes. This repo had
copies of some of it: the container profile, the overlay store settings, the journal relay, sudo, cursor
and frame-rate defaults, a DrKonqi drop-in and the restore's progress writer.

## Decision

- The desktop image uses `webtop-nix.nixosModules.container` and its options (`storeOverlay`,
  `journalRelay`, `adminTools`, `sudo`) and the module defaults; it keeps only what is the pod's:
  the kubelet-owned mounts, `trusted-users` by group, `fsync-store-paths`, the chart's Nix include.
- It enables `sudo` and `adminTools` explicitly, so the desktop behaves as before.
- The progress writer is webtop-nix's `webtop-restore-progress`; the reader (the agent, the tray
  job) and the restore scripts stay here, because they use the pod's volume layout and the controller.
- New desktop features that need neither the controller nor the agent go to webtop-nix first.

## Consequences

- A pin bump of webtop-nix moves these defaults; the built `nix.conf`, the Selkies unit environment and
  the user's groups are the checks that a bump changed nothing it should not.
- The agent still reads `/run/webtop/restore/progress.json`: that file is a contract with webtop-nix.
