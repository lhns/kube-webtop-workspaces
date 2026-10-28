# 0005 — Power actions in the desktop call the controller, authenticated by pod-bound tokens

- Status: Accepted
- Date: 2026-10-28

## Context

Shutting down inside the desktop ends PID 1, but a Deployment restarts the container, so a
shutdown would behave like a reboot. The desktop itself uses standard systemd and logind power
management ([webtop-nix ADR 0003](https://github.com/lhns/webtop-nix/blob/main/docs/adr/0003-a-real-logind-session-with-standard-power-management.md)); something must turn "powered off" into "stopped" without a polling
daemon and without giving the root-capable desktop write access to the cluster.

## Decision

- The image runs **a systemd unit whose stop hook fires on the `poweroff`, `reboot` and `halt` targets, and maps sleep to poweroff**. It
  calls the controller's power API and waits for its acknowledgement before PID 1 may exit.
- Calls carry the pod's **projected ServiceAccount token** (own audience). The controller checks it
  with a TokenReview and acts only on the desktop the calling pod belongs to. No sidecar, no
  per-pod RBAC.
- Stop and suspend record the reason and set the desktop off; the controller scales it to 0.
  Reboot requests a rollout restart. A clean container exit without a recorded stop also counts as
  a stop.
- Suspend means session save and stop until checkpoint/restore works.

## Consequences

- Shutdown, idle shutdown and the logout dialog behave as on a real machine.
- A desktop can stop or restart only itself.
- The controller's API must be reachable from desktop pods; the chart's NetworkPolicy allows
  exactly that.
