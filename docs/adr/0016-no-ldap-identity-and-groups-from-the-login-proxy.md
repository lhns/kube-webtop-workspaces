# 0016 — LDAP removal: identity and groups come only from the login proxy

- Status: Accepted
- Date: 2026-11-05
- Supersedes: 0003

## Context

0003 made an LDAP group the source of who may have a desktop, with a periodic sync, a bind secret
and an LDAP client in the controller. The login proxy already authenticates every request and sends
the user, groups and display name as headers (0002), and the gateway already checks the groups.
Two sources of truth for access meant two places to keep right, and a directory credential in the
controller.

## Decision

- **Access is the gateway's check of the group headers on every request**, nothing else. kww has no
  directory client, no OIDC client and no per-user tokens. A user removed at the provider is
  refused from their next request.
- **The controller no longer authorizes.** It starts whatever is `On`. The `Authorized` condition,
  the LDAP sync and the `ldap` stop reason are gone.
- **The display name comes from a header** (`gateway.displayNameHeader`). The gateway stores it in
  `spec.displayName`; only a start copies it to `status.displayName`, so a running pod's template
  never changes with it. Header text is untrusted: it is cleaned for the passwd GECOS field and is
  never rendered in a page.
- **Removing a user's desktop is explicit:** an admin CLI in the controller binary (`desktops list`,
  `stop`, `remove`). `remove` deletes the Desktop and keeps the PVC; no command deletes data.
- **Optional automatic stop:** `desktop.stopAfterUnseen` (off by default, at least one day) stops a
  desktop whose owner the gateway has not served for that long, using `spec.lastSeen`, which the
  gateway writes at most hourly. Data is kept. Nothing is ever removed automatically.
- Desktops are still never stopped by default (0003); creation on first visit and navigation-only
  starts are unchanged.

## Consequences

- One source of truth for access; no directory credential, TLS or sync to operate.
- A removed user's running desktop stays up until stopped by hand or by `stopAfterUnseen`.
- `stopAfterUnseen` cannot tell a removed user from a login-provider outage; it skips while the
  gateway has no ready pod and rejects values under a day.
- `spec.lastSeen` and `spec.displayName` are written by the gateway, which may not write status;
  every owner request can cause a spec patch (throttled), which the controller must treat as
  template-neutral.
- Rejected: token introspection, refresh tokens and back-channel logout (they need kww to be an OIDC
  client holding per-user tokens, and an expired token looks like a removed user); an LDAP reap
  job; an admin web page (more surface than a CLI).
