# 0001 — Controller and gateway: one binary, two Deployments, the Desktop CRD between them

- Status: Accepted
- Date: 2026-10-28

## Context

One part must face users (route traffic, show a boot screen); another must hold write access to
Deployments, Services and PVCs and the LDAP credentials. A single process would put cluster write
access behind an internet-facing HTTP endpoint.

## Decision

- One Go binary, run as **two Deployments**: `--mode=gateway` (user-facing, no write access to core
  resources, no secrets) and `--mode=controller` (reconciler, LDAP sync, power API).
- A namespaced **`Desktop` CRD** (`webtop.lhns.github.io/v1alpha1`), one per user, is the only
  interface between them: the gateway sets `spec.powerState`, the controller does the rest and
  reports progress in `status`.
- Both run with several replicas; the controller elects a leader for reconciling and syncing,
  every replica serves the power API.

## Consequences

- A compromised gateway can start desktops but cannot create workloads or read secrets.
- `kubectl get desktops` shows every desktop's state; metrics and the boot screen read the same
  status.
- One image and one release cycle for both parts.
