# 0002 — One hostname, the user's identity from forward-auth headers

- Status: Accepted
- Date: 2026-10-28

## Context

Each user must reach only their own desktop. Per-user hostnames need wildcard DNS and certificates,
per-user ingress objects that differ between ingress controllers, and per-user authorisation
rules, and still need something in the path that knows whether a desktop is running.

## Decision

- **One hostname.** The gateway reverse-proxies HTTP and WebSockets to the caller's own desktop.
- The user and groups come from **headers set by the cluster's forward-auth** (names configurable).
  Members of a configurable admin group may open `/u/<user>/`.
- The gateway trusts those headers only from the ingress controller: a NetworkPolicy admits nothing
  else (desktop pods in particular), and `trustedProxyCIDRs` adds a second check.

## Consequences

- Works with any ingress controller and any forward-auth that sets user headers.
- All desktop traffic, video included, passes the gateway: one extra hop, and the gateway must run
  redundantly.
- The NetworkPolicy is part of the security model: without it, anything reaching the gateway can
  claim to be any user.
