# 0020 — Chart defaults reach the desktop as webtop-nix's defaults directory

- Status: Accepted; amends [0019](0019-desktop-features-that-need-no-kubernetes-come-from-webtop-nix.md)
- Date: 2026-11-11

## Context

The chart passed a time zone and a locale as pod env, which `webtop-init` copied into a file the image named in
`localeEnvironmentFiles`. Admins also want language, formats, KDE settings and home-manager settings as defaults for
every desktop. Env does not carry files, and each change needs a new pod template. webtop-nix now reads a
defaults directory (its ADR 0015) and has dropped `localeEnvironmentFiles`.

## Decision

- `desktop.defaults` (`timeZone`, `locale`, `language`, `formats`, `xdgConfig`, `homeModule`) renders one ConfigMap,
  mounted read-only at `/run/webtop/defaults-source` through `desktop.extraVolumes` / `extraVolumeMounts`. The image
  sets `services.webtop.defaults.source` to that path. Its keys map to the directory's `session.conf`, `xdg/<name>`
  and `home.nix`; what they mean is webtop-nix's.
- The chart puts a checksum of the data in `desktop.podAnnotations`: a change alters the pod template, so running
  desktops show `updatePending` and pick it up at their next start (ADR 0006). No controller change.
- `desktop.timeZone` and `desktop.locale`, and the `WEBTOP_TIME_ZONE` / `WEBTOP_LOCALE` env, are removed. The chart
  fails if they are still set, and on `xdgConfig.plasma-localerc`, which webtop-locale owns.
- Nothing is enforced and the ConfigMap holds no secrets: every desktop user can read it.

## Consequences

- A site moves from `desktop.timeZone` / `desktop.locale` to `desktop.defaults` in the same change as the chart bump.
- An admin change applies per desktop at its next start, not to running sessions.
- The chart knows webtop-nix's entry names, and nothing of what they do.
