# 0004 — The controller never deletes or owns a volume

- Status: Accepted
- Date: 2026-10-28

## Context

A desktop's PVC holds the user's home and Nix store. On some clusters deleting a PV destroys the
volume regardless of `reclaimPolicy`, and an ownerReference makes a PVC disappear with its owner.

## Decision

- No code path deletes a PVC, and the controller's Role has no `delete` verb on PVCs; a chart test
  fails if one appears.
- PVCs get labels and annotations, **never an ownerReference**. Deleting a `Desktop` removes its
  Deployment and Service, not its PVC.
- An existing PVC is adopted only if it carries the desktop label; an unlabelled one with the same
  name blocks the start instead of being taken over.
- PVCs are named after the user, so a removed and re-added user gets their old home back.

## Consequences

- Removing a user's data is always a deliberate manual step by an administrator.
- Volumes of departed users accumulate until someone removes them.
- Migrating existing data is possible by labelling a prepared PVC.
