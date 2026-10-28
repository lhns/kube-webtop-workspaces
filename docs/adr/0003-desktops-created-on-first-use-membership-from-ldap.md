# 0003 — Desktops created on first use, membership from an LDAP group

- Status: Accepted
- Date: 2026-10-28

## Context

Who gets a desktop should follow group membership, without a list in git and without workloads for
users who never log in. A desktop must never start or stop behind the user's back, including from a
browser tab that is still open after a shutdown.

## Decision

- A user's `Desktop`, PVC, Service and Deployment are **created on their first visit**, if they are
  in the configured LDAP group.
- The controller **re-checks the group periodically**. Removal stops the desktop and keeps its data.
  An LDAP error or an empty result never counts as removal.
- **Only a top-level navigation starts a desktop.** WebSocket and XHR requests to a stopped desktop
  get 503; after a recorded stop the user sees "desktop is off, start it?" instead of an automatic
  start. While a desktop starts, the browser is held on a boot screen that streams the real stages.
- Desktops are never stopped by default; stopping is the user's (power actions, their own idle
  setting).

## Consequences

- No per-user objects or configuration in git; adding someone to the group is enough.
- Running desktops hold memory until their users stop them; monitoring shows idle ones.
- A user re-added to the group regains access only after the next sync.
