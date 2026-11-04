# kube-webtop-workspaces

Persistent per-user browser desktops on Kubernetes. Every authorized user gets their own desktop pod and
volume. The pod is created on first use, shows a boot screen while it starts, and stops only when the
user shuts it down (or idles out, per their own desktop settings).

## Components

One Go binary (`cmd/kube-webtop-workspaces`), run as two Deployments:

- **gateway** (`--mode=gateway`): sits behind a forward-auth proxy that sets the user and groups
  headers. It reverse-proxies each user to their own desktop (HTTP and websockets), starts it on a
  top-level navigation, and serves the boot screen, the "desktop is off" page and, after Sleep, a "desktop is asleep" page with a Wake button. Admins can open any
  desktop at `/u/<user>/`.
- **single apps**: `/apps/<name>` (or `/u/<user>/apps/<name>` for admins) streams one application
  of the desktop instead of the whole desktop, where `<name>` is a desktop file id such as
  `org.kde.dolphin` or `firefox`. The gateway has the in-pod agent place and start the app on a second
  screen, then redirects to the desktop page `/#display2-right`. Limits: one app at a time (opening
  another releases the first, which keeps running on the main desktop), audio plays on the main page
  only, any application installed for the user is allowed, and an unknown id gets a 404 page.
  Reloading the URL does not start a second instance while the app is running. `/apps/` (without a
  name) is an overview page of the desktop's applications with icons and a "running" badge, each
  linking to its `/apps/<name>`. Contract:
  [section 7.6](docs/contract.md#76-single-app-baseappsname), ADR 0010.
- **controller** (`--mode=controller`): reconciles `Desktop` resources
  (`webtop.lhns.github.io/v1alpha1`) into a PVC, Service and Deployment, and checks LDAP group
  membership. It also serves the power/report API that the in-pod agent uses to report shutdowns,
  reboots, boot progress and session activity. PVCs are never deleted by the controller.

Also:

- **desktop image** (`ghcr.io/lhns/kube-webtop-workspaces-desktop`, below): NixOS with systemd as
  PID 1, the Plasma desktop from [webtop-nix](https://github.com/lhns/webtop-nix) and the in-pod
  agent.
- **Helm chart** (`charts/kube-webtop-workspaces`).

**Interface:** [docs/contract.md](docs/contract.md) covers the CRD, labels, the power/report API,
pod environment, gateway behaviour, configuration, RBAC and metrics.

## Desktop image

Built from `flake.nix` with webtop-nix's image builder (nix2container, its ADR 0005;
[ADR 0015](docs/adr/0015-desktop-image-layered-by-webtop-nix-builder.md)): `nix build .#image`, then
`nix run .#image.copyTo -- --dest-compress-format zstd --dest-compress-level 6 docker://<ref>`. The layers are a split of
the system's closure, stable to volatile (base, toolkits, data, KDE, Firefox, webtop-nix packages, the KWin
graft, the baked flake sources, kww's own packages, config) with the Nix DB and root files on top, so an
update that leaves nixpkgs alone pulls only the layers it changed. The build prints the table
(`nix build .#image.layerSplit`); CI adds the layers new against `main` to the run summary. Published for amd64 and arm64 as one multi-arch index
([ADR 0014](docs/adr/0014-multi-arch-images-built-natively-published-as-one-index.md)); `packages.<system>`
for `x86_64-linux` and `aarch64-linux`, each built on its own architecture.

| path | what |
| --- | --- |
| `image/webtop-init` | entrypoint (static busybox): mounts, then `exec /sbin/init` |
| `image/nixos/container.nix` | NixOS as a container, Nix store settings, journal relay |
| `image/nixos/desktop.nix` | `services.webtop` (webtop-nix) wired to the pod contract |
| `image/nixos/hooks.nix` | agent units: power hook, session save, suspend, image-update restore and reinstall |
| `image/nixos/home-manager.nix` | webtop-nix's `services.webtop.nixProfile` (patched Nix, `packages.json`, starter flake, `webtop-notify`; its ADR 0006) fitted to the pod: home on the volume, restore ordering, progress |
| `image/webtop-restore-progress.sh`, `image/restore-progress.nix` | progress of the software restore after an image update, for the tray job and the boot page |
| `image/nixos/identity.nix` | renames the desktop user (uid 1000) to the login name at activation |
| `image/webtop-nix-lower` | run by webtop-init: the store overlay after an image update (partial invalidation, else reset) |
| `image/webtop-nix-partial` | invalidates what a new image store breaks in the overlay, rolls back on failure |
| `image/webtop-nix-restore.sh` | copies the users' paths from the old store overlay into the new one |
| `agent/` | in-pod agent (Go, its own module; packaged by `image/agent.nix`) |

**Entrypoint.** `webtop-init` makes `/` rprivate, binds the image's `/nix` read-only at
`/.lower/nix`, mounts an overlayfs on `/nix/store` (lower: the image's store, upper:
`/persist/nix/upper`, `userxattr,index=off,xino=off`), binds `/persist/nix/var` on `/nix/var/nix`, mounts the user's home at its paths
(below), writes the `WEBTOP_*` env to `/run/webtop/env` and extra binary caches (`WEBTOP_NIX_*`) to
`/run/webtop/nix.conf` (systemd does not pass the container env to units) and execs systemd. Stop signal `SIGRTMIN+3`. `WEBTOP_INIT_DEBUG=1` parks the container on a
failed step instead of exiting; `WEBTOP_INIT_SKIP_OVERLAY=1` boots on the image's store alone.
systemd replaces PID 1's stdout, so a unit follows the journal into a fifo that webtop-init relays to
the container log (`kubectl logs`).

**Nix store** (ADR 0008). The daemon opens `/nix/store` as a `local-overlay` store whose lower is
the image's store (with its own DB) and whose upper and DB are on the volume; clients, root included,
use the daemon. The user's packages (`nix profile`, home-manager) cost only what they add.
An image update changes the lower store, which the overlay store does not expect:
`webtop-nix-lower` (run by webtop-init before the overlay is mounted) compares
`/webtop-image-stamp` (a hash of the image's store paths and their NAR hashes) with
`/persist/nix/lower-stamp`, and on a change records each user's profile manifest and runs
`webtop-nix-partial`. That compares the upper DB with the old image's path list
(`/persist/nix/lower-paths`, headed by that image's stamp; a list of another image is not used) and
the new image's DB: rows of paths the new image dropped or ships
with other bytes, rows whose files are not whole in the upper, and everything referring to them are
invalidated with `nix-store --delete --ignore-liveness` on the upper opened as a plain local store;
upper entries that would merge with or hide a path of the new image (copy-up leftovers, whiteouts,
copies of paths the image now ships, unfinished writes) and the invalidated paths move to
`/persist/nix/trash-*`, with a snapshot of the DB. A post-condition then checks every row again.
Everything else stays in place. If anything fails (or two attempts were interrupted), the pre-run
state is put back (if even that fails, its trash dir is kept for the restore) and the overlay is
reset as a whole: the old upper/DB move to the trash, the
store starts fresh, the old GC roots are kept. The reason is in the container log. After boot, in
the background (no shutdown step), `webtop-nix-restore.service` (`image/webtop-nix-restore.sh`)
copies the users' live paths (profiles, home-manager generations, GC roots) that are not in the new
store from the trash; paths only the old image had are substituted, and a root whose closure ended
up split over several trash dirs is copied path by path. It skips when there is no room (free space after the copy must stay above a
reserve). Only then `webtop-nix-trash.service` deletes the old overlay, and
`webtop-nix-reinstall.service` runs for what did not come back (roots that depend on image-only paths
such as the patched KWin, or no network): it reinstalls the recorded flake packages of the profile (by
locked URL) and runs `webtop-home-manager` (relock, `home-manager switch`, which reinstates everything
declared), with desktop notifications. Packages built locally without a flake reference are lost only
if they cannot be restored.

**User identity** (ADR 0011). The desktop user (uid 1000) is named after the desktop
(`WEBTOP_DESKTOP`), with home `/home/<name>` and the LDAP display name (`WEBTOP_DISPLAY_NAME`) as
GECOS. A name that starts with a digit or is an account or group of the image (`root`, `nixbld1`,
`wheel`, …) falls back to `user`, and the Desktop gets condition `LoginName=False`. The image declares
the account as `user` and an activation snippet renames it; units, polkit and Nix refer to uid 1000
or a group, never the name. The data always lives in `/persist/home/user`; `/home` is a tmpfs with a
bind mount of it at every path listed in `~/.config/webtop/home-paths` plus the current
`/home/<name>`, which webtop-init adds at every start. Delete a line once nothing refers to that path
anymore; it disappears at the next start. Lines other than `/home/<valid login name>`, more than 16,
or a symlinked file are ignored (logged by webtop-init). `status.loginName` and `status.homePaths`
show the result.

*Existing volumes.* The first start creates `home-paths` with the new path and `/home/user`, so profile
links, `home.homeDirectory` and KDE configs that name `/home/user` keep working. home-manager itself
checks the user name and home, so at every start `webtop-home-manager-template.service` rewrites
`~/.config/home-manager/{flake.nix,home.nix}` for the current name while they are unmodified: what it
last wrote (hashes in `~/.local/state/webtop/templates.sha256`) or byte-identical to the starter
template rendered for a name in `home-paths`. An edited flake is left alone; if it still names
another user, the journal of that unit and a desktop notification list the lines to change
(`homeConfigurations."…"`, `home.username`, `home.homeDirectory`). Until then a plain
`home-manager switch` fails. A failed rename at
activation leaves the account `user` with `/home/user` mounted; the reason is reported (condition
`LoginName`, `RenameFailed`). The unmodified check is the
`webtop-template write|unmodified` helper, for other units that update files the image put into the
home too.

Remove `/home/user` from `home-paths` only once nothing refers to it. Known referrers: `~/.nix-profile`
and other profile links (`~/.local/state/nix/profiles`), home-manager's `home.homeDirectory` and the
generated `hm-session-vars.sh`, older generations kept as GC roots (a rollback to them expects
`/home/user`), and KDE, Firefox and other app configs. To check:
`grep -rl /home/user ~/.config ~/.local/state ~/.mozilla 2>/dev/null` and
`ls -l ~/.nix-profile ~/.local/state/nix/profiles`; switch home-manager after updating its config so
the current generation refers to the new path.

**home-manager.** On first boot, only if `~/.config/home-manager` is absent (and once), a starter
flake is created there (webtop-nix `modules/nix-profile/starter/`) and switched to. Its `nixpkgs`, `home-manager` and `webtop` inputs come from the
image (no download): flake inputs resolve indirect refs through the global registry only, so the image
sets `flake-registry` to the upstream registry with its own pins in front (`webtop` is a store path of
webtop-nix `modules/nix-profile/webtop-flake/`).
The template is written for the login name; its generation is prebuilt for the placeholder `user`, so the
first switch builds the few derivations that carry the name. Building a changed config may substitute build
tools from cache.nixos.org. A flake in a git repo ignores untracked files (`git add` them; the profile
hook does `git add --intent-to-add packages.json`), and so is `local.nix`. After an image update the
image-registry inputs are relocked automatically (`webtop-home-manager`, which the reinstall unit runs);
`webtop` whenever it is not the image's current copy.

**Software** (ADR 0013). One declaration, `~/.config/home-manager/packages.json`, which the
starter flake's `webtop.homeModules.packages` module installs and points `nix profile` at:

| to | run |
| --- | --- |
| install | `nix profile add nixpkgs#hello` (any flake installable) |
| remove | `nix profile remove hello` (names and store paths; `--regex` and `--all` match packages.json only) |
| list | `nix profile list` (declared packages, then undeclared profile elements, marked `Backend:`) |
| upgrade | `nix profile upgrade --all`: relocks packages from other flakes; nixpkgs ones follow the image |
| undo | `nix profile history`, `nix profile rollback [--to N]`: versions of packages.json only |
| try without installing | `nix shell nixpkgs#hello` |

Nix is patched (webtop-nix `modules/nix-profile/nix/`): with `profile-backend = declaration:<file>` (set in the user's
`~/.config/nix/nix.conf` by that module), `add` builds the installable and records it in the
file the way `manifest.json` records an element, then runs `profile-hook`
(`webtop-profile-hook`: `home-manager switch`); if the hook fails the file is restored. Store
paths and elements the file does not declare stay in the profile as before (home-manager's own
generation is one); a profile element of the same name moves into the file when added, and a store path named like a
declared package is refused. If any part
of a command fails, all of it is reverted. `--profile-backend profile` or `--profile <path>` gives
stock behaviour for one call. Entries are locked: a package from a local `path:` flake picks up edits only with
`nix profile upgrade`, and such a flake needs its `flake.lock` before the first `add` (`nix flake
lock`), since writing it changes the directory's hash. Packages in `home.nix` work as before. KDE add-ons from Plasma's "Get New…" dialogs live in
`~/.local/share`, outside the declaration. The GUI for installing is the Nix Software Center (it runs `nix profile`); there is no Discover.

Your own additions go into `~/.config/home-manager/local.nix`, a module the starter flake imports if it
exists and the image never writes. In a git repo `git add` it, like `packages.json`: an untracked file
is invisible to the flake.

`webtop-home-manager-template.service` keeps the starter files current with `webtop-template`: an
unmodified `flake.nix`/`home.nix` (what the unit last wrote, or the template rendered for another of the
home's names) is rewritten for the login name and the current template. An older template that
imported `packages.nix` is upgraded the same way: `packages.nix` is removed if unmodified, and
`packages.json` stays. Files you edited are never rewritten, nor is a starter whose `packages.nix` you
edited or whose `~/.config/nix/nix.conf` is your own (the module writes that file); an edited flake
that still imports `./packages.nix` keeps working. For these a notification says once per template
version that a newer one exists. `webtop-home-manager-update` prints the difference to the current
template and changes nothing; `webtop-home-manager-update --apply` backs up each differing file as
`<file>.bak-<time>`, installs the template and switches, and restores files and hashes if the switch
fails. Nothing is merged: move your edits over by hand (into `local.nix`, where they survive).

**Power.** The agent reports stops to the controller before PID 1 exits (contract section 8.2):

| stop | systemd target | report |
| --- | --- | --- |
| pod deleted (`SIGRTMIN+3`) | `halt.target` | `stop`/`kube-stop`, after saving the Plasma session |
| Shut down in Plasma, `systemctl poweroff` | `poweroff.target` | `stop`/`user`, or `stop`/`idle` (heuristic below) |
| Restart | `reboot.target` | `reboot` |
| Sleep | `suspend.target` | no stop: the session is frozen, the pod keeps running (below); only the fallback is marker + poweroff, then `suspend`/`suspend` |

`webtop-power-hook.service` (a boot-time `RemainAfterExit` unit whose `ExecStop` runs at shutdown,
while the token, `resolv.conf` and `hosts` mounts still exist; it reads the target from the queued
jobs) posts the request and retries until the 200 or 60 s
(`WEBTOP_POWER_HOOK_TIMEOUT`); it never fails the shutdown. `idle` vs `user` is a heuristic: the
agent tracks input idleness from KWin (`ext-idle-notify-v1` on the socket named by `WAYLAND_DISPLAY`
in the user manager's environment); a poweroff after at least 60 s without
input (`WEBTOP_IDLE_REASON_THRESHOLD`; PowerDevil's idle action needs a minute or more) is `idle`.
A Plasma shutdown saves the session itself (restore is on by default);
`webtop-session-save.service` saves it on a pod deletion or a Sleep that falls back to poweroff: it calls ksmserver's
`saveCurrentSession` over the user's session bus, which writes the "saved by user" session, and
copies that group to "saved at previous logout" in `~/.config/ksmserverrc`, the one Plasma restores. A shell `systemctl poweroff` does not save it, as on a real machine.

**Sleep.** `systemd-suspend.service` runs `webtop-agent suspend` (ADR 0009). It freezes the user
manager's `plasma-*.service` units in `session.slice` and every unit in `app.slice` except Selkies
(`systemctl --user freeze`, the cgroup freezer), reports `asleep: true`, and blocks. Selkies, the
session bus, audio, `background.slice` and the agent stay up, so the readiness probe passes and
a browser can still connect. A new Selkies connection thaws exactly what was frozen and the unit
exits 0, so logind and PowerDevil resume as on hardware. The units are listed in
`/run/webtop/frozen-units`; `webtop-agent thaw` thaws them.

Wake rule: the agent records the Selkies session ids when Sleep starts and wakes on a session
id it has not seen, or when the session count rises above the lowest it has seen since. Clients
open at Sleep do not wake it, but their reload (new id) or closing and reopening the tab does. If
Selkies' sessions carry no ids, a reload faster than the 0.5 s poll cannot be told from the open tab.

A stop while asleep (pod deletion, poweroff) thaws first: the agent thaws on SIGTERM,
`systemd-suspend.service` is ordered after `webtop-session-save.service` so it stops first, and the
latter thaws again before saving. If freezing fails, or `WEBTOP_SUSPEND_MODE=poweroff` is set in the
pod environment (default `freeze`), Sleep saves the session and powers off with reason `suspend`.

**Boot progress and activity.** `webtop-agent run` reports the stage and progress from systemd's
boot progress, the user's `plasma-workspace.target` and Selkies' port, then `ready`. Afterwards it
reports `connectedClients` (Selkies `GET /api/sessions`), `idleSeconds` and `asleep` every 30 s and when the
client count or sleep state changes.

**Path prefix.** The gateway strips `/u/<user>` and requires the trailing slash; the Selkies web
client loads its assets and opens `api/websockets` relative to the page's directory, so it needs no
prefix setting. Selkies' `SELKIES_SUBFOLDER` must stay empty: it would make the server expect the
prefix the gateway has already stripped.

**Selkies settings** (`image/nixos/desktop.nix`): plain HTTP; audio sink, cursor path and library
wrapper come from webtop-nix. Clipboard, file transfer and browser cursors are webtop-nix's defaults.
Precedence, low to high: the image's defaults < the cluster (`desktop.selkies` and the tab title
`desktop.title`, delivered through `/run/webtop/selkies.env`; contract, "Selkies settings") < the user's
allowlisted `~/.config/webtop/selkies.env` (`services.webtop.userSettings`: stream quality and input
feel). The per-user file is a convenience, not a security boundary, since the user can edit their own
systemd user units; settings that must hold (file transfers off) cannot rely on it. A changed setting or
title applies on the desktop's next start (ADR 0006).

**KWin patch.** KWin 6.8 dropped QPainter compositing, so without a GPU it needs a patch to render
nested under Selkies: [webtop-nix](https://github.com/lhns/webtop-nix) grafts a patched KWin (LinuxServer's
software-presentation patch) over the stock one (`services.webtop.kwinSoftwarePresentation`, its ADR 0004).
Plasma is otherwise plain nixpkgs, which this flake follows from webtop-nix: bumping the webtop-nix pin moves it; the patch only applies to KWin 6.8.x,
and the evaluation fails on another minor version. Building that KWin takes 25–45 minutes on a GitHub runner, so
the `desktop-image` job caches it (`packages.<system>.kwin`, keyed by its store path, so per architecture).

**Known limits**

- The Nix build sandbox is off (`pivot_root` fails with `EPERM` in the user-namespaced pod).
- No real suspend: sleep freezes the session's processes. The pod keeps its RAM and CPU request while
  asleep, and PowerDevil's idle shutdown does not fire while frozen. logind offers sleep only with a
  writable `/sys/power/state`, which is read-only in a pod; webtop-init binds a stand-in over it
  (best effort, logged).
- Session restore on Wayland relaunches apps; it restores only what ksmserver can (X11 apps via
  Xwayland, apps that support KDE's session restore), not window contents of arbitrary apps.
- The desktop user has passwordless sudo: root in the pod is unprivileged on the node (user
  namespace).

## Development

```sh
make generate   # deepcopy, CRD (config/crd), RBAC (config/rbac)
make build vet
make test       # unit + envtest; setup-envtest downloads kube-apiserver/etcd
test/nix-store/run.sh  # image-update store restore against real overlay stores (privileged docker)
```

envtest has no Windows binaries. On Windows, run the tests in a Linux container:

```sh
tar -cf - --exclude=.git . | docker run -i --rm -v kww-gocache:/root/.cache -v kww-gomod:/go/pkg/mod golang:1.26 \
  sh -c 'mkdir /src && tar -xf - -C /src && cd /src && make test'
```

## CI and releases

`ci.yaml` checks every PR; a push to `main` or a `vX.Y.Z` tag then publishes, only when every check is
green. `main` publishes `0.0.0-dev.<sha7>`; a tag publishes images `vX.Y.Z` (and `latest`) and chart
`X.Y.Z`. Both images are amd64+arm64 indexes and a tag's digest is the index's. The `desktop-image` job
builds the image on PRs too, on an x86 and an arm runner, and boots each under docker
(`.github/scripts/boot-test.sh`): it passes only when the agent reports Ready, which needs KWin
compositing. `nixpkgs-sync` requires `inputs.nixpkgs` to follow webtop-nix's. `prune.yaml` lists old dev versions weekly and deletes them only with the repo
variable `PRUNE_ENABLED=true`.

## License

Apache-2.0
