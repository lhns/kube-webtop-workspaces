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
  `org.kde.dolphin` or `firefox`. The gateway has the in-pod agent place and start the app on its own
  screen, then redirects to the desktop page `/#display<n>-right-<name>`. Each app gets its own screen
  and tab, up to `desktop.maxAppScreens` (default 4) at once. Limits: audio plays on the main page
  only, any application installed for the user is allowed, and an unknown id gets a 404 page.
  Reloading the URL does not start a second instance while the app is running. `/apps/` (without a
  name) is an overview page of the desktop's applications with icons and a "running" badge, each
  linking to its `/apps/<name>`. Contract:
  [section 7.6](docs/contract.md#76-single-app-baseappsname), ADRs 0010 and [0018](docs/adr/0018-each-app-gets-its-own-screen.md).
- **controller** (`--mode=controller`): reconciles `Desktop` resources
  (`webtop.lhns.github.io/v1alpha1`) into a PVC, Service and Deployment, and stops desktops of long-unseen
  users when configured. It also serves the power/report API that the in-pod agent uses to report shutdowns,
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
| `image/webtop-init` | entrypoint (static busybox): mounts, runs webtop-nix's `webtop-store-init` and `webtop-sys-power`, then execs `/sbin/init` or the system built from the admin's `system.nix` (`webtop-defaults-boot`) |
| `image/nixos/container.nix` | webtop-nix's `nixosModules.container` fitted to the pod: kubelet-owned mounts, `storeOverlay`, `journalRelay`, passwordless sudo, Nix settings by group |
| `image/nixos/desktop.nix` | `services.webtop` (webtop-nix) wired to the pod contract |
| `image/nixos/hooks.nix` | agent units (power hook, restore job) and the options that tie session exit and Sleep to the agent |
| `image/nixos/home-manager.nix` | webtop-nix's `services.webtop.nixProfile` (patched Nix, `packages.json`, starter flake, `webtop-notify`; its ADR 0006) fitted to the pod: home on the volume, progress |
| `image/nixos/identity.nix` | renames the desktop user (uid 1000) to the login name at activation |
| `agent/` | in-pod agent (Go, its own module; packaged by `image/agent.nix`) |

**Entrypoint.** `webtop-init` makes `/` rprivate, runs webtop-nix's `webtop-store-init` (the store overlay on `/nix/store`,
state in `/persist/nix`), mounts the user's home at its paths
(below), binds webtop-nix's `/sys/power` stand-in (`webtop-sys-power`), writes the `WEBTOP_*` env to `/run/webtop/env` and extra binary caches (`WEBTOP_NIX_*`) to
`/run/webtop/nix.conf` (systemd does not pass the container env to units) and execs systemd. Stop signal `SIGRTMIN+3`. `WEBTOP_INIT_DEBUG=1` parks the container on a
failed step instead of exiting; `WEBTOP_INIT_SKIP_OVERLAY=1` boots on the image's store alone.
systemd replaces PID 1's stdout, so a unit follows the journal into a fifo that webtop-init relays to
the container log (`kubectl logs`).

**Nix store** (ADR 0008, amended by [ADR 0019](docs/adr/0019-desktop-features-that-need-no-kubernetes-come-from-webtop-nix.md)).
This repo sets `storeOverlay.enable` and runs webtop-nix's `storeOverlay.init` from `webtop-init`, with the state in `/persist/nix`.
The overlay, its layout and the image-update handling are webtop-nix's ([README, "Store overlay"](https://github.com/lhns/webtop-nix#store-overlay), its ADR 0013).
This repo shows the restore: the agent reads `/run/webtop/restore/progress.json` (`nixProfile.progress`), reports it to the
controller, and `webtop-restore-job` shows it in the tray.

**User identity** (ADR 0011). The desktop user (uid 1000) is named after the desktop
(`WEBTOP_DESKTOP`), with home `/home/<name>` and the login proxy's display name header (`WEBTOP_DISPLAY_NAME`) as
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
links, `home.homeDirectory` and KDE configs that name `/home/user` keep working. `webtop-init` passes
the paths as `WEBTOP_HOME_PATHS` (`/run/webtop/env`, `nixProfile.templateEnvironmentFiles`), so webtop-nix's
starter-flake upgrade rewrites an unmodified starter for the current name and treats one rendered for a
listed name as unmodified ([README, "Software"](https://github.com/lhns/webtop-nix#software)). A failed rename at
activation leaves the account `user` with `/home/user` mounted; the reason is reported (condition
`LoginName`, `RenameFailed`).

Remove `/home/user` from `home-paths` only once nothing refers to it. Known referrers: `~/.nix-profile`
and other profile links (`~/.local/state/nix/profiles`), home-manager's `home.homeDirectory` and the
generated `hm-session-vars.sh`, older generations kept as GC roots (a rollback to them expects
`/home/user`), and KDE, Firefox and other app configs. To check:
`grep -rl /home/user ~/.config ~/.local/state ~/.mozilla 2>/dev/null` and
`ls -l ~/.nix-profile ~/.local/state/nix/profiles`; switch home-manager after updating its config so
the current generation refers to the new path.

**Software** (webtop-nix [README, "Software"](https://github.com/lhns/webtop-nix#software), its ADRs 0006 and 0014; [ADR 0019](docs/adr/0019-desktop-features-that-need-no-kubernetes-come-from-webtop-nix.md)).
`nixProfile.enable` gives each user `packages.json`, `nix profile`, the starter flake and the Software Center. This repo fits it
to the pod (`image/nixos/home-manager.nix`): `homeStoragePath = /persist/home/user`, whatever paths the home is mounted at, and
`templateEnvironmentFiles` for `WEBTOP_HOME_PATHS`. Changing `homeStoragePath` makes every unmodified starter flake look edited,
since existing homes' `templates.sha256` are keyed by it.

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
The session is saved by webtop-nix's `sessionSave` ([README, "Sleep and session save"](https://github.com/lhns/webtop-nix#sleep-and-session-save)); `terminationGracePeriodSeconds` must cover it.

**Sleep.** The image sets webtop-nix's `sleep.mode = "freeze"` ([README, "Sleep and session save"](https://github.com/lhns/webtop-nix#sleep-and-session-save), its ADR 0012;
[ADR 0019](docs/adr/0019-desktop-features-that-need-no-kubernetes-come-from-webtop-nix.md)). The agent, Selkies and the session bus stay up, so the readiness
probe passes and a browser can still connect. This repo adds the controller side: a `sleep.hooks` entry (`webtop-agent sleep-report asleep|awake`)
reports `asleep: true` at the freeze and `false` at the thaw, the steady reports repeat the state from `/run/webtop/frozen-units`, and a
Sleep that powers off (`WEBTOP_SUSPEND_MODE=poweroff` in the pod environment, or a failed freeze) leaves `/run/webtop/stop-reason` = `suspend`,
which the power hook reports as reason `suspend`.

**Boot progress and activity.** `webtop-agent run` reports the stage and progress from systemd's
boot progress, the user's `plasma-workspace.target` and Selkies' port, then `ready`. Afterwards it
reports `connectedClients` (Selkies `GET /api/sessions`), `idleSeconds` and `asleep` every 30 s and when the
client count or sleep state changes.

**Path prefix.** The gateway strips `/u/<user>` and requires the trailing slash. Selkies' `SELKIES_SUBFOLDER` must stay empty
(webtop-nix leaves it unset): it would make the server expect the prefix the gateway has already stripped.

**Desktop defaults** (`desktop.defaults`: time zone, language, formats, KDE config, a home-manager module): a ConfigMap
mounted at `/run/webtop/defaults-source`, which the image names as `services.webtop.defaults.source`. Precedence,
validation and the override order are webtop-nix's ([README, "Admin defaults"](https://github.com/lhns/webtop-nix#admin-defaults));
contract, "Desktop defaults". No browser input.

**Selkies settings** (`image/nixos/desktop.nix`): plain HTTP (`SELKIES_ENABLE_HTTPS=false`); the rest of
the Selkies setup is webtop-nix's. Precedence, low to high: the image's defaults < the cluster (`desktop.selkies` and the tab title
`desktop.title`, delivered through `/run/webtop/selkies.env`; contract, "Selkies settings") < the user's
allowlisted `~/.config/webtop/selkies.env` (`services.webtop.userSettings`: stream quality and input
feel). The per-user file is a convenience, not a security boundary, since the user can edit their own
systemd user units; settings that must hold (file transfers off) cannot rely on it. A changed setting or
title applies on the desktop's next start (ADR 0006).

**Known limits**

- The Nix build sandbox is off (`pivot_root` fails with `EPERM` in the user-namespaced pod).
- No real suspend: Sleep freezes the session, so the pod keeps its RAM and CPU request while asleep. `/sys/power/state` is
  read-only in a pod; `webtop-init` runs webtop-nix's `webtop-sys-power` over it (best effort, logged). The desktop's own limits
  are in the [webtop-nix README](https://github.com/lhns/webtop-nix).
- The desktop user has passwordless sudo (`sudo.enable`): root in the pod is unprivileged on the node (user namespace).

## Features and third-party changes

A single desktop's features come from [webtop-nix](https://github.com/lhns/webtop-nix) (see its README for the full list, including every change to a third-party
program, and [ADR 0016](docs/adr/0016-desktop-features-come-from-webtop-nix-options.md) and
[ADR 0019](docs/adr/0019-desktop-features-that-need-no-kubernetes-come-from-webtop-nix.md) for the split). This repo adds what needs
Kubernetes or the agent:

- Controller and gateway: desktops created on first use, one hostname with identity and groups from the login proxy's headers
  ([ADR 0017](docs/adr/0017-no-ldap-identity-and-groups-from-the-login-proxy.md)), the Desktop CRD and its status, power actions and
  updates on the next start.
- The in-pod agent: boot progress, activity and power reports (including asleep/awake from webtop-nix's sleep hook), the apps API
  for single-app streaming, the restore's tray job and progress on the boot page.
- The pod's entrypoint (`webtop-init`): the env files, the login-name home, and the calls to webtop-nix's `webtop-store-init` and
  `webtop-sys-power`.

This repo changes no third-party program.

## Development

```sh
make generate   # deepcopy, CRD (config/crd), RBAC (config/rbac)
make build vet
make test       # unit + envtest; setup-envtest downloads kube-apiserver/etcd
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
compositing; the job caches webtop-nix's patched KWin (`packages.<system>.kwin`, per architecture). `nixpkgs-sync` requires `inputs.nixpkgs` to follow webtop-nix's. `prune.yaml` lists old dev versions weekly and deletes them only with the repo
variable `PRUNE_ENABLED=true`.

## License

Apache-2.0
