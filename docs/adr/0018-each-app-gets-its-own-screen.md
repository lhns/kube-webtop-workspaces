# 0018 — Each app gets its own screen; the agent allocates the numbers and the cap

- Status: Accepted
- Date: 2026-11-05

## Context

`/apps/<name>` showed one app on `SELKIES-2`; opening another released the first. Users want several
apps side by side, one browser tab each. Each visible screen costs a KWin output, a capture and an
encoder, so the number has to be bounded.

## Decision

- The agent allocates display numbers, starting at 3 (2 is Selkies' own sidebar button). The map
  app to number is persisted and a number is never reused while the file lives, so a reload or a
  restored tab can only reach its own app, never another.
- The cap (`desktop.maxAppScreens`, env `WEBTOP_MAX_APP_SCREENS`) is enforced in the agent, the one
  place that knows the live screens. Screens opened in the last minute count too, because their page
  has not connected yet. An app that already has a screen is never refused.
- The gateway redirects to `#display<n>-right-<app>` and shows a 409 page when over the cap.
- Raising a screen goes through the gateway (`POST <base>/.webtop/focus`), same authz and same-origin
  rule as the other state-changing routes, never starting or waking a desktop. `reload.js` calls it.

## Consequences

- The cap is a limit of the gate, not of the desktop: a reload or a restored tab skips the agent, so
  more screens than the cap can be open.
- Numbers only grow; the file lives in the pod's state directory, so a desktop restart starts again at 3.
- An agent that predates this answers `204`; the gateway then uses display 2, as before.
