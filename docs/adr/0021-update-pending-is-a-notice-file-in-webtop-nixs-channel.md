# 0021 — The pending update reaches the user as a notice file in webtop-nix's channel

- Status: Accepted
- Date: 2026-11-12

## Context

A pending template update ([0006](0006-updates-apply-on-the-next-start.md)) applies at the next start, but only
the Desktop status shows it, so a user who never restarts never learns of it. webtop-nix has a notice channel,
root-owned files in `/run/webtop/notices/` shown as notifications (its ADR 0019).

## Decision

- The agent (root, in the pod) writes `/run/webtop/notices/update-pending.json` while `status.updatePending` is
  true and removes it when it is false. Title "Updates are ready", body "Restart your desktop to apply them.",
  action `restart` (Plasma's confirmed Restart, which ends in `reboot.target` and the `reboot` power report that
  applies the template).
- The value reaches the agent in the answer to its report: a report sent with `notices: true` gets `200
  {"updatePending": bool}` instead of `204`. A pod annotation through the downward API would need the controller to
  patch pods, a new RBAC verb; the report path exists and is already authenticated per pod.
- This is the only notice sent from here. No expiry, no other senders, no broadcast API.

## Consequences

- The notice follows the stored status, so it lags a template change by a reconcile plus up to one report (30 s).
- An image without webtop-nix's watcher ignores the file; an older controller answers `204` and nothing is written.
- The wording is fixed English in the agent; a site cannot change it without an image change.
