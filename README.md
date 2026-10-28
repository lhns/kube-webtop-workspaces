# kube-webtop-workspaces

Persistent per-user browser desktops on Kubernetes. Every authorized user gets their own desktop pod and
volume. The pod is created on first use, shows a boot screen while it starts, and stops only when the
user shuts it down (or idles out, per their own desktop settings).

## Components

One Go binary (`cmd/kube-webtop-workspaces`), run as two Deployments:

- **gateway** (`--mode=gateway`): sits behind a forward-auth proxy that sets the user and groups
  headers. It reverse-proxies each user to their own desktop (HTTP and websockets), starts it on a
  top-level navigation, and serves the boot screen and the "desktop is off" page. Admins can open any
  desktop at `/u/<user>/`.
- **controller** (`--mode=controller`): reconciles `Desktop` resources
  (`webtop.lhns.github.io/v1alpha1`) into a PVC, Service and Deployment, and checks LDAP group
  membership. It also serves the power/report API that the in-pod agent uses to report shutdowns,
  reboots, boot progress and session activity. PVCs are never deleted by the controller.

- **desktop image** (`ghcr.io/lhns/kube-webtop-workspaces-desktop`, below): NixOS with systemd as
  PID 1, the Plasma desktop from [webtop-nix](https://github.com/lhns/webtop-nix) and the in-pod
  agent.
- **Helm chart** (`charts/kube-webtop-workspaces`).

**Interface:** [docs/contract.md](docs/contract.md) covers the CRD, labels, the power/report API,
pod environment, gateway behaviour, configuration, RBAC and metrics.

## Desktop image

Built from `flake.nix` with nix2container: `nix build .#image`, then
`nix run .#image.copyTo -- docker://<ref>`. amd64 only (the Selkies/pixelflux wheels are x86_64).

| path | what |
| --- | --- |
| `image/webtop-init` | entrypoint (static busybox): mounts, then `exec /sbin/init` |
| `image/nixos/container.nix` | NixOS as a container, Nix store settings, journal relay |
| `image/nixos/desktop.nix` | `services.webtop` (webtop-nix) wired to the pod contract |
| `image/nixos/hooks.nix` | agent units: power hook, session save, suspend, image-update reinstall |
| `agent/` | in-pod agent (Go, its own module; packaged by `image/agent.nix`) |

**Entrypoint.** `webtop-init` makes `/` rprivate, binds the image's `/nix` read-only at
`/.lower/nix`, mounts an overlayfs on `/nix/store` (lower: the image's store, upper:
`/persist/nix/upper`, `userxattr`), binds `/persist/nix/var` on `/nix/var/nix` and `/persist/home` on
`/home`, writes the `WEBTOP_*` env to `/run/webtop/env` (systemd does not pass the container env to
units) and execs systemd. Stop signal `SIGRTMIN+3`. `WEBTOP_INIT_DEBUG=1` parks the container on a
failed step instead of exiting; `WEBTOP_INIT_SKIP_OVERLAY=1` boots on the image's store alone.
systemd replaces PID 1's stdout, so a unit follows the journal into a fifo that webtop-init relays to
the container log (`kubectl logs`).

**Nix store** (ADR 0008). The daemon opens `/nix/store` as a `local-overlay` store whose lower is
the image's store (with its own DB) and whose upper and DB are on the volume; clients, root included,
use the daemon. The user's packages (`nix profile`, home-manager) cost only what they add.
An image update changes the lower store, which the overlay store forbids: webtop-init compares
`/webtop-image-stamp` (a hash of the image's store paths) with `/persist/nix/lower-stamp`, and on a
change records each user's profile manifest, moves the old upper/DB aside and starts fresh.
`webtop-nix-reinstall.service` then reinstalls the recorded flake packages (by locked URL) and runs
`home-manager switch --flake ~/.config/home-manager` if that flake exists, with a desktop
notification; `webtop-nix-trash.service` deletes the old upper. Paths built locally and installed
without a flake reference are lost on an image update.

**Power.** The agent reports stops to the controller before PID 1 exits (contract section 8.2):

| stop | systemd target | report |
| --- | --- | --- |
| pod deleted (`SIGRTMIN+3`) | `halt.target` | `stop`/`kube-stop`, after saving the Plasma session |
| Shut down in Plasma, `systemctl poweroff` | `poweroff.target` | `stop`/`user`, or `stop`/`idle` (heuristic below) |
| Restart | `reboot.target` | `reboot` |
| Sleep | `suspend.target` | marker + poweroff, then `suspend`/`suspend`, after saving the session |

`webtop-power-hook.service` (a boot-time `RemainAfterExit` unit whose `ExecStop` runs at shutdown,
while the token, `resolv.conf` and `hosts` mounts still exist; it reads the target from the queued
jobs) posts the request and retries until the 200 or 60 s
(`WEBTOP_POWER_HOOK_TIMEOUT`); it never fails the shutdown. `idle` vs `user` is a heuristic: the
agent tracks input idleness from KWin (`ext-idle-notify-v1` on the socket named by `WAYLAND_DISPLAY`
in the user manager's environment); a poweroff after at least 60 s without
input (`WEBTOP_IDLE_REASON_THRESHOLD`; PowerDevil's idle action needs a minute or more) is `idle`.
A Plasma shutdown saves the session itself (restore is on by default);
`webtop-session-save.service` saves it on a pod deletion or sleep: it calls ksmserver's
`saveCurrentSession` over the user's session bus, which writes the "saved by user" session, and
copies that group to "saved at previous logout" in `~/.config/ksmserverrc`, the one Plasma restores. A shell `systemctl poweroff` does not save it, as on a real machine.

**Boot progress and activity.** `webtop-agent run` reports the stage and progress from systemd's
boot progress, the user's `plasma-workspace.target` and Selkies' port, then `ready`. Afterwards it
reports `connectedClients` (Selkies `GET /api/sessions`) and `idleSeconds` every 30 s and when the
client count changes.

**Path prefix.** The gateway strips `/u/<user>` and requires the trailing slash; the Selkies web
client loads its assets and opens `api/websockets` relative to the page's directory, so it needs no
prefix setting. Selkies' `SELKIES_SUBFOLDER` must stay empty: it would make the server expect the
prefix the gateway has already stripped.

**Selkies settings** (`image/nixos/desktop.nix`): plain HTTP, audio from the monitor of a PipeWire
null sink named `output` (a pod has no sound card), `XCURSOR_PATH` for pixelflux's cursor loader, and
a wrapper that puts the libraries pixelflux dlopens on `LD_LIBRARY_PATH`. Clipboard, file transfer
(to `~/Desktop`) and browser cursors are Selkies' defaults.

**Known limits**

- The Nix build sandbox is off (`pivot_root` fails with `EPERM` in the user-namespaced pod).
- No real suspend: sleep is mapped to session save + poweroff. logind offers sleep only with a
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
```

envtest has no Windows binaries. On Windows, run the tests in a Linux container:

```sh
tar -cf - --exclude=.git . | docker run -i --rm -v kww-gocache:/root/.cache -v kww-gomod:/go/pkg/mod golang:1.26 \
  sh -c 'mkdir /src && tar -xf - -C /src && cd /src && make test'
```

## License

Apache-2.0
