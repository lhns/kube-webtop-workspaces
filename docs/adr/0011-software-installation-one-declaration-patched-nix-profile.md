# 0011 — Software installation: one declaration, patched `nix profile`

- Status: Accepted
- Date: 2026-11-02

## Context

A desktop had three ways to install software that did not know about each other: KDE Discover
(on NixOS only Flatpak and KDE add-ons), `nix profile` (recorded only in the profile's manifest)
and home-manager's `home.nix`. An image update had to reinstall imperative profile entries
separately from home-manager, and nothing showed one list of what the user installed.

## Decision

- **One declaration.** `~/.config/home-manager/packages.json`, next to `home.nix`, lists the
  user's packages. The starter flake's `packages.nix` module reads it into `home.packages`:
  `flake:nixpkgs` entries from the configuration's own nixpkgs (the image's), others from their
  locked flake (`builtins.getFlake`, pure). Format: `{"version": 1, "elements": {name: element}}`,
  an element being what `manifest.json` records per element (`originalUrl`, `url`, `attrPath`,
  `outputs`, `priority`), without the derived `storePaths` and `active`.
- **`nix profile` edits it.** Nix is patched (a local fork of `src/nix/profile.cc`, kept generic
  because that keeps it small): `nix profile` has backends, chosen by the client-side setting
  `profile-backend` (`profile` = stock, `profile:<path>`, or `declaration:<path>`; `--profile P`
  is short for `profile:P`, and giving both flags is an error). In the declaration backend
  `add` resolves and builds flake installables and records them in the file, `remove`/`upgrade`
  edit it, `list` shows it together with the profile's undeclared elements (marked, stock format).
  Store paths and undeclared elements go to the profile as before, which is what home-manager's
  own activation does, so any home-manager version works unmodified.
- **A hook applies changes.** `profile-hook` runs after every change, in either backend; if it
  fails, the change is reverted (the file's previous contents, or the profile's previous
  generation). It must exit 0 only once the change is fully applied; it is required for the
  declaration backend. The image's hook runs `home-manager switch` for declaration changes,
  re-activating the previous generation if that fails, and does nothing for profile changes,
  which keeps it cheap for home-manager's own installs and avoids recursion.
- **History is the declaration's.** Each change keeps the file as a generation of the profile
  `declaration` next to the user's profile (hand edits become a generation before the next change);
  `history`, `rollback` and `wipe-history` use Nix's generation code on it, and `rollback` runs the
  hook. It covers the package list only; `home-manager generations` remains the tool for the rest.
- **Scoped to the user.** `packages.nix` writes the two settings into the user's
  `~/.config/nix/nix.conf` (directly, not `nix.settings`, which home-manager validates with an
  unpatched Nix). The system and root keep stock behaviour; deleting the module restores it.
- **No Discover.** KDE add-ons from Plasma's "Get New…" dialogs (KNewStuff) remain; they live in
  `~/.local/share`, outside the declaration.

## Consequences

- One list (`nix profile list`, packages.json, git-trackable) and one apply step; an image update
  reinstates everything declared with `home-manager switch`.
- The image builds `nix-cli` from source with the patch, pinned to Nix 2.34; a nixpkgs bump to
  another Nix minor version needs the patch rebased.
- Earlier starter flakes are migrated at boot (their `nix profile` packages move into the file);
  customized setups keep stock-shaped behaviour until they add the module.
- No GUI for now: nix-software-center reads the profile manifest in a pre-2.20 format and depends
  on an external package database that is empty for current nixpkgs revisions.
