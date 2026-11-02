# 0011 — The desktop user has the login name; its home is mounted at every path it had

- Status: Accepted
- Date: 2026-11-02

## Context

The desktop's Linux account was a fixed `user` (uid 1000, home `/home/user`), so Plasma, the shell
and every tool showed `user` instead of the person. The account is declared at image build time,
but the name is known only per pod. On NixOS, activation regenerates `/etc/passwd` from the declared
users at every boot, so editing it before `/sbin/init` is lost. A home also accumulates absolute paths
to itself (Nix profile links, home-manager's `home.homeDirectory`, KDE configs), so renaming the
directory breaks existing data, and a user's name may change.

## Decision

- **Login name** = the Desktop's name (`sanitize(spec.user)`, `WEBTOP_DESKTOP`), if it is a valid
  login name (starts with a letter) and not an account or group of the image. Otherwise the user
  stays `user`, and the controller sets condition `LoginName=False`. **Display name** (GECOS) from
  `ldap.displayNameAttribute` at each start (`status.displayName`, `WEBTOP_DISPLAY_NAME`), else the
  login name.
- **uid/gid stay 1000/100**, so ownership on volumes never changes.
- **Rename at activation.** The image declares the placeholder `user`; an activation snippet ordered
  after `users`, `groups` and `etc` runs `usermod -l <name> -d /home/<name> -c <gecos>` on it
  (`image/nixos/identity.nix`). The container's rootfs, `/etc` included, is fresh at every start and
  nothing re-runs activation in a pod, so the rename holds for the pod's lifetime. Rejected:
  pre-seeding passwd (update-users-groups.pl keys by name and keeps the old entry next to the
  declared one), runtime userdb records (a second source of uid 1000 for PAM, sudo and Nix), and
  declaring the name at build time (one image per user).
- **Everything else refers to uid 1000**, never the name: units (`User=1000`), lingering, the polkit
  power rule (group `webtop`, webtop-nix), Nix `trusted-users` (`@wheel`), the agent (looks up uid
  1000), scripts (`id -nu 1000`). webtop-init decides the name, since it must mount the home before
  init and passwd does not exist yet; the image's account and group names are baked in at build time.
- **Home: bind mounts, no symlinks or renames.** The data stays at `/persist/home/user` (also on new
  volumes). `/home` is a tmpfs holding only bind mounts of it, one per path in
  `~/.config/webtop/home-paths` plus the current `/home/<name>`, which is added to the file at every
  start. The user deletes a line once nothing refers to that path; it is gone at the next start. A
  missing file is created with the current path, plus `/home/user` on a volume that already had a
  home. As root at boot, webtop-init ignores the file or its directories when they are symlinks or not
  regular, and accepts only `/home/<login name>` lines that are not image accounts, at most 16.
- The agent reports the login name and home paths; they show in `status.loginName` and
  `status.homePaths`.

## Consequences

- Old absolute paths keep working for as long as the user keeps them listed; no data is rewritten.
- home-manager refuses a config naming another user. The image rewrites its starter flake for the new
  name only while it is unmodified (byte-identical to what it wrote or to the template for an old
  name); an edited one stays as it is, and the user is told which lines to change.
- The starter home-manager template is written with the real name; the generation prebuilt into the
  image is the placeholder's, so the first switch builds the few derivations that carry the name.
- Relies on update-users-groups.pl (asserted: not sysusers or userborn) and on activation running once
  per container.
- A desktop whose name is an image account or starts with a digit runs as `user`.
