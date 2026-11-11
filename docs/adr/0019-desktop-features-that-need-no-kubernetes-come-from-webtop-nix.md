# 0019 — Desktop features that need no Kubernetes come from webtop-nix

- Status: Accepted; supersedes [0009](0009-sleep-freezes-the-session.md) and [0013](0013-software-installation-one-declaration-patched-nix-profile.md), amends [0008](0008-per-user-nix-store-as-a-local-overlay-store.md) and [0016](0016-desktop-features-come-from-webtop-nix-options.md); amended by [0020](0020-chart-defaults-reach-the-desktop-as-webtop-nixs-defaults-directory.md)
- Date: 2026-11-10

## Context

[0016](0016-desktop-features-come-from-webtop-nix-options.md) left Sleep, the session save and the store
overlay's image-update handling here, and the image reached into webtop-nix's units to fit them to the pod.
None of that needs Kubernetes. webtop-nix now ships it behind options and documented extension points
(its README, "Extension points"; its ADR 0012 Sleep and session save, 0013 the store overlay and image
updates, 0014 `nix profile` recording into one declaration).

## Decision

- The image uses those options and no unit override of webtop-nix: `sessionEnvironmentFiles`,
  `sessionStopCommands`, `sessionConfKeys`, `nixProfile.templateEnvironmentFiles`, `sleep.mode`,
  `sleep.environmentFiles`, `sleep.hooks`, `sessionSave`, `storeOverlay.persistPath` and the `storeOverlay.init` and
  `sleep.init` scripts, which `webtop-init` runs.
- What is the pod's stays here: the agent's boot, idle, apps and power-hook reports, the restore job in the tray,
  and a `sleep.hooks` entry (`webtop-agent sleep-report`) that tells the controller `asleep`/`awake`.
- The contract with webtop-nix is its documented markers and variables: `/run/webtop/stop-reason` = `suspend`
  (the power hook reports reason `suspend`), `/run/webtop/frozen-units` (the steady report's `asleep`),
  `WEBTOP_SUSPEND_MODE` and `WEBTOP_HTTP_PORT` from `/run/webtop/env`, `/run/webtop/restore/progress.json`.
- The store overlay scripts' tests (`test/nix-store`) and the end-to-end image tests stay here and run
  webtop-nix's scripts at the revision the flake locks.

## Consequences

- The freeze, session save, store overlay and restore code is maintained once, in webtop-nix; a pin bump
  moves it, and the image tests are the check that a bump changed nothing it should not.
- The on-volume layout (`/persist/nix`) and `WEBTOP_SUSPEND_MODE` are unchanged, so volumes and the pod contract are.
- A missing hook is a webtop-nix option, not an override here.

## Amendment 2026-11-11

The store overlay scripts' tests and the `nix profile` and starter template image tests moved to webtop-nix
(`tests/`, run on its own image). Here the boot test keeps what needs the pod: the agent's reports, the identity
rename and the controller contract.
