# 0009 — Sleep freezes the session instead of powering off

- Status: Accepted; supersedes the "suspend means session save and stop" line of [0005](0005-power-actions-call-the-controller-with-pod-bound-tokens.md)
- Date: 2026-11-01

## Context

A pod cannot suspend, so Sleep powered the desktop off and the next visit cold-booted it. systemd 262
in the pod has a writable cgroup v2 and the cgroup freezer, which stops a unit's processes
without touching their state.

## Decision

- `systemd-suspend.service` runs `webtop-agent suspend`, which freezes the Plasma session units and
  the apps (`plasma-*.service` in `session.slice`, everything in `app.slice`) through the user
  manager, blocks, and thaws on the next new Selkies connection. It then exits 0, so logind
  and PowerDevil resume as on hardware.
- Selkies, the session bus, audio, `background.slice` and the agent stay running: Selkies is the
  readiness probe and the wake trigger.
- The pod stays `Running`; the agent reports `asleep` (`status.asleep`). The controller does not stop it.
- The frozen units are recorded in `/run/webtop/frozen-units`. Every stop path thaws them first
  (SIGTERM to the agent, `webtop-session-save`'s `ExecStop`), because ksmserver must answer the
  session save.
- If freezing fails, or `WEBTOP_SUSPEND_MODE=poweroff`, Sleep saves the session and powers off with
  reason `suspend`, as before.

## Consequences

- Waking is instant and loses nothing, but the pod keeps its memory and CPU request while asleep.
- PowerDevil's idle shutdown cannot fire while frozen.
- Wake detection depends on Selkies' `/api/sessions`.
