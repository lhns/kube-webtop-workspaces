# 0006 — Updates apply on the next start

- Status: Accepted
- Date: 2026-10-28

## Context

A new image or pod template would roll every Deployment at once and end every running session,
which contradicts "never stopped unless the user stops it".

## Decision

- The controller hashes the rendered pod template. It writes a new template only while the
  desktop is stopped or when the user reboots; otherwise it sets `status.updatePending`.

## Consequences

- Running sessions are never interrupted by an update.
- Desktops that are never restarted keep running old images; `updatePending` and its metric show
  them.
