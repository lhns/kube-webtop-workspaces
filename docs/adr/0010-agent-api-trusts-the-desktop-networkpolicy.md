# 0010 — The agent's gateway-facing API trusts the desktop NetworkPolicy

- Status: Accepted
- Date: 2026-11-01

## Context

Single-app streaming (`/apps/<name>`) needs the gateway to make the in-pod agent place and start an
application, so the agent gets an HTTP endpoint. [0005](0005-power-actions-call-the-controller-with-pod-bound-tokens.md)
authenticates the opposite direction with TokenReview, but the desktop pod has no API-server access
and must not get it. A token or per-desktop secret would need distribution to both sides.

## Decision

- The agent serves its API on a fixed pod port (8081, `agent` in the Service), unauthenticated.
- The desktop NetworkPolicy admits only gateway pods to the desktop's ports, as it already does for
  the web client. The gateway decides who may use which desktop from the forward-auth identity.
- The API can only do what the session user can do by clicking (show an application of their own
  desktop). Selkies on `desktop.port` already gives anyone who can reach it the whole desktop, so
  the agent port adds no new trust boundary.

## Consequences

- With `networkPolicy.enabled=false` the cluster must isolate desktop pods itself, as it must for
  the web client.
- New endpoints on this port must stay within what the desktop user could do anyway; anything
  stronger needs a real credential and a superseding ADR.
