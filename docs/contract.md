# kube-webtop-workspaces contract

The interface between the controller/gateway binary, the desktop image (and its in-pod agent) and
the Helm chart. Anything not stated here is an implementation detail and may change.

Version: `v1alpha1`. Go module: `github.com/lhns/kube-webtop-workspaces`.

## 1. Components

| component | binary | replicas | job |
| --- | --- | --- | --- |
| gateway | `kube-webtop-workspaces --mode=gateway` | 2+, stateless | auth-header routing, start on navigation, boot/off pages, reverse proxy |
| controller | `kube-webtop-workspaces --mode=controller` | 2+, leader election | reconcile `Desktop`s, unseen sweep (leader only); power/report API (every replica); admin CLI (`desktops`, section 12) |
| desktop | the desktop image | 0/1 per user | NixOS + systemd; the in-pod agent talks to the power/report API |

Everything lives in **one namespace** (`namespace` in the config, default: the pod's namespace).

## 2. CRD `Desktop`

- Group/version: **`webtop.lhns.github.io/v1alpha1`**, kind `Desktop`, plural `desktops`, short
  name `dt`, **namespaced**. The group is fixed (it names the project owner, like the module path,
  and is collision-free); it is not a chart value.
- Subresource **`status`** is enabled: spec and status have separate RBAC. The gateway may only
  touch spec; only the controller writes status.
- CRD YAML: `config/crd/webtop.lhns.github.io_desktops.yaml` (controller-gen). The chart copies it
  to `crds/`.

### Name

`metadata.name` = `sanitize(spec.user)` (section 4). The controller sets phase `Error` on a
Desktop whose name is not `sanitize(spec.user)`.

### spec

| field | type | notes |
| --- | --- | --- |
| `user` | string, required, immutable (CEL `self == oldSelf`) | the user ID exactly as the auth header has it |
| `powerState` | `On` \| `Off`, default `Off` | desired state |
| `restartRequestedAt` | RFC 3339 time, optional | set to request a restart ("reboot"); applies pending updates |
| `lastSeen` | RFC 3339 time, optional | the gateway served the owner, written at most hourly (section 7.1); `desktop.stopAfterUnseen` reads it. In spec because the gateway may not write status |
| `displayName` | string, up to 512 | the owner's name from the login proxy's header (`gateway.displayNameHeader`), cleaned like `status.displayName`; a start copies it there |

### status

| field | type | meaning |
| --- | --- | --- |
| `phase` | `Off` \| `Starting` \| `Running` \| `Stopping` \| `Error` | summary; the gateway proxies only in `Running` |
| `stage` | string | human-readable current step (section 3.4) |
| `bootProgress` | int 0–100 | 0 when off |
| `message` | string | detail for `Error` or a desktop that cannot start |
| `url` | string | proxy target, `http://<svc>.<ns>.svc:<port>`; set by the controller |
| `podName`, `podUID` | string | the current desktop pod |
| `agentReady` | bool | the agent of `podUID` reported ready |
| `lastStart` | time (µs) | when `powerState` last went `On` (or a restart was applied) |
| `lastStop` | `{time (µs), reason}` | last recorded stop; reasons in section 3.3. Both µs fields are compared with each other |
| `lastReportTime` | time | last accepted agent report |
| `appliedTemplateHash` | string | hash of the pod template on the Deployment |
| `updatePending` | bool | desired hash ≠ applied hash while running |
| `observedRestartAt` | time | last `spec.restartRequestedAt` the controller applied |
| `connectedClients` | int | from the agent |
| `idleSeconds` | int | from the agent |
| `asleep` | bool | the agent froze the session (Sleep); the pod stays `Running`. Reset with the pod |
| `loginName` | string | the desktop user's login name, from the agent (section 8.3); kept while off |
| `homePaths` | []string, ≤ 16 | paths the user's home is mounted at, from the agent; kept while off |
| `loginNameError` | string, ≤ 512 | why the rename to the desktop name failed, from the agent (section 8.3) |
| `softwareRestore` | object | `active`, `done`, `total` (steps), `failed`: the software restore after an image update, from the agent (section 8.3); never blocks `Ready`. The boot page says so while `active`. Reset with the pod |
| `displayName` | string | `spec.displayName` as the current or last pod started with it: no control characters, `:` `,` quotes or backslashes, at most 128 characters. Only a start (or applied restart) copies it, so a running pod's template never changes with the header |
| `lastLogout` | time | the session logged out without a shutdown and the user's choice (default `gateway.ssoLogoutDefaults`) says to sign out at the provider; only set with `gateway.logoutUrl`. Reset with the pod (section 7.8) |
| `lastLock` | time | the user used Lock and their choice (default `gateway.ssoLogoutDefaults.onLock`) says to sign out at the provider; only set with `gateway.lockLogsOut` and `gateway.logoutUrl`. Reset with the pod (section 7.9) |
| `imagePull` | `{image, startedAt?, lastSizeBytes?, lastDurationSeconds?}` | the desktop image's pulls, for the boot page's download estimate (section 3.4); kept while off |
| `observedGeneration` | int | |
| `conditions` | `[]metav1.Condition` | `Ready`, `LoginName` |

Conditions:

- `Ready`: `True` iff phase `Running`.
- `LoginName` (once the agent reported `loginName`): `True` (`DesktopName`) when the desktop user has
  the desktop's name; `False` when the image fell back to `user` because the name starts with a digit
  (`InvalidLoginName`), is an account of the image (`ReservedName`) or could not be renamed to
  (`RenameFailed`, with the reason from `status.loginNameError`).

## 3. Controller behaviour

### 3.1 Created objects

| object | name | owner | notes |
| --- | --- | --- | --- |
| Deployment | `<prefix><desktop>` | Desktop (controller ownerReference) | replicas 0/1, strategy `Recreate` |
| Service | `<prefix><desktop>` | Desktop (controller ownerReference) | ClusterIP, ports `http` (`desktop.port`) and `agent` (8081), selects the desktop label |
| PVC | `<prefix><desktop>` | **none, ever** | created on the first start; mounted at `storage.mountPath` |

`<prefix>` is `names.prefix` (default `desktop-`); it must start with a letter and be ≤ 22 chars so
names stay ≤ 63.

Labels on all three and on the pod template:

| key | value |
| --- | --- |
| `webtop.lhns.github.io/desktop` | desktop name |
| `app.kubernetes.io/name` | `webtop-desktop` |
| `app.kubernetes.io/instance` | desktop name |
| `app.kubernetes.io/managed-by` | `kube-webtop-workspaces` |

These are interface, not decoration: the chart's NetworkPolicies select desktop pods on
`app.kubernetes.io/name=webtop-desktop` + `app.kubernetes.io/managed-by=kube-webtop-workspaces`
(the desktop policy itself and the controller's power API ingress), the Service and Deployment
select on `webtop.lhns.github.io/desktop` + `managed-by`, and the power API checks `managed-by` and
`desktop` (section 8.1). `desktop.podLabels` and `storage.labels` cannot override them.

Annotations:

| key | on | value |
| --- | --- | --- |
| `webtop.lhns.github.io/user` | PVC, Deployment, pod template | original user ID |
| `webtop.lhns.github.io/template-hash` | Deployment | hash of the rendered pod template |
| `webtop.lhns.github.io/restarted-at` | pod template | `spec.restartRequestedAt` when applied (not hashed) |

Deleting a `Desktop` garbage-collects the Deployment and Service; the **PVC stays**. A re-created
Desktop of the same name re-adopts it.

**PVC rules (hard):** the code has no PVC delete call, PVCs never get an ownerReference, and the
controller role has no `delete`/`deletecollection` on PVCs (asserted by a test on
`config/rbac/controller/role.yaml`). An existing PVC of the right name is adopted only if it carries
`webtop.lhns.github.io/desktop=<desktop>`; otherwise phase `Error` and nothing starts. To migrate
data into a pre-created PVC, label it before the first start. The controller only adds missing
labels/annotations to an adopted PVC; it never changes its spec.

### 3.2 Reconcile

1. **Validate:** name = `sanitize(spec.user)`, else `Error`.
2. *(Authorization is the gateway's: the controller starts whatever is `On`.)*
3. **PVC** when starting (section 3.1).
4. **Service** and **Deployment** (always, once valid).
   - Desired pod template rendered from the config (section 5); hash = SHA-256 of the rendered pod
     template without the `restarted-at` annotation (replicas excluded).
   - Template written when the Deployment is at replicas 0, when it goes 0→1, or when
     `spec.restartRequestedAt > status.observedRestartAt` (rollout; also stamps `restarted-at`).
   - Otherwise only `spec.replicas` is patched; a differing hash sets `status.updatePending=true`.
     **A running desktop is never restarted by an update.**
5. **Replicas:** 1 iff `powerState=On`, else 0.
6. **Stop recording:** when the controller scales 1→0 and `status.lastStop.time < status.lastStart`
   (nobody recorded a reason), it records reason `admin`, or `error` while the phase is `Error`. The unseen sweep records `unseen`.
7. **Clean exit:** while `powerState=On`, a container termination (current or last state) with an
   exit code in `controller.cleanExitCodes` (default `[0]`) and `finishedAt > status.lastStart` is a
   stop with reason `exit`: `powerState=Off`, Deployment to 0. Not an exit stop if
   `spec.restartRequestedAt` is newer than that container's `startedAt` (a reboot), or if
   `status.lastStop.time ≥ status.lastStart`.
8. **Status:** phase/stage/progress (section 3.4), `url`, `podName/podUID`, conditions.

Only the leader reconciles. Status is written with merge patches of the fields it owns.

### 3.3 Stop reasons

| reason | set by |
| --- | --- |
| `user` | agent (shutdown/reboot from the desktop) |
| `idle` | agent (PowerDevil idle action) |
| `suspend` | agent (Sleep fallback: save + poweroff, when freezing fails or `WEBTOP_SUSPEND_MODE=poweroff`; webtop-nix writes `/run/webtop/stop-reason`, the power hook reads it) |
| `kube-stop` | agent (PID 1 got the stop signal from a pod deletion); never recorded |
| `exit` | controller (clean exit without a recorded stop) |
| `admin` | controller (`powerState` set `Off` by someone else) |
| `error` | controller (`powerState` set `Off` by someone else while phase is `Error`: the gateway's error page, section 7.5) |
| `unseen` | controller (`desktop.stopAfterUnseen`, section 6) |

### 3.4 Phase and stage

| condition | phase | stage |
| --- | --- | --- |
| `Off`, no pod | `Off` | `Stopped` |
| `Off`, pod exists | `Stopping` | `Stopping` |
| `On`, no pod yet | `Starting` | `Scheduling` |
| `On`, no live pod, the previous pod still terminating | `Starting` | `StoppingPrevious` |
| pod unschedulable | `Starting` | `Scheduling` (+ `message`) |
| container waiting (`ContainerCreating`), a `Pulling` event of the pod has no later `Pulled` | `Starting` | `PullingImage` |
| container waiting, a `FailedAttachVolume` event has no later `SuccessfulAttachVolume` (and no pull is running) | `Starting` | `AttachingVolume` |
| container waiting, otherwise | `Starting` | `CreatingContainer` |
| `ErrImagePull`/`ImagePullBackOff`/`InvalidImageName`/`CrashLoopBackOff`/`CreateContainerConfigError`/`CreateContainerError`/`RunContainerError` | `Error` | the waiting reason (+ `message`) |
| container terminated, restart pending | `Starting` | `Booting` |
| container running, no agent report | `Starting` | `Booting` |
| agent reported, not ready | `Starting` | agent's `stage` (free text, ≤ 64 characters) |
| pod `Ready` and (`agentReady` or `controller.requireAgentReady=false`) | `Running` | `Ready`, progress 100 |
| invalid name / foreign PVC | `Error` | `Error` |

The three waiting stages all have progress 15. The events are those of the current pod (by
`involvedObject.uid`), read from a cache restricted to the desktop namespace and to events about
pods, with an index on the pod UID; events with the reasons `Pulling`, `Pulled`,
`FailedAttachVolume` and `SuccessfulAttachVolume` of a desktop pod trigger a reconcile of its
Desktop. Event order is by `lastTimestamp` (then `eventTime`, `firstTimestamp`); a tie ends the
failure or pull.

**Image pull record.** While a pod exists the controller keeps `status.imagePull` for the desktop
container's image reference: `startedAt` is the `Pulling` event's time while that pull runs; a
kubelet `Pulled` message `Successfully pulled image "<ref>" in <duration> (…). Image size: <n> bytes.`
sets `lastDurationSeconds` and `lastSizeBytes` (the first duration; "already present on machine" is
no pull). A record of another image ref is dropped. While pulling an image with no recorded
duration, the numbers of any other Desktop that recorded the same ref are used.

## 4. Usernames

`sanitize(user)`:

1. lowercase;
2. every character outside `[a-z0-9]` becomes `-`;
3. runs of `-` collapse to one; leading/trailing `-` are trimmed;
4. truncated to 40 characters, then trailing `-` trimmed again;
5. empty → rejected (gateway 400 page).

**Login name.** The desktop user's login name is the Desktop name when it starts with a letter and is
not an account or group of the image; otherwise `user` (condition `LoginName`, ADR 0011).

**Collision:** if a Desktop `sanitize(user)` exists with a different `spec.user`, the gateway
refuses (409 page) and never touches it. User IDs are immutable at the provider, so the mapping is stable.

## 5. Desktop pod

Rendered from the `desktop` and `storage` config sections. Fixed parts:

- Pod: `hostUsers: false` (`desktop.hostUsers`), `automountServiceAccountToken: false`,
  `enableServiceLinks: false`, `serviceAccountName: desktop.serviceAccountName`,
  `runtimeClassName`, `terminationGracePeriodSeconds` (default 420; must exceed the setup unit's 5 min stop timeout plus the power hook
  timeout plus the session save).
- Container `desktop`: `runAsUser: 0`, `runAsNonRoot: false`, capabilities add `SYS_ADMIN` and `SYS_NICE`,
  `procMount: Unmasked`, `seccompProfile: RuntimeDefault` (or `desktop.seccompProfile`), `appArmorProfile: Unconfined`
  (`desktop.securityContext` replaces the whole container securityContext when set).
- Readiness probe: TCP on `desktop.port`.
- Ports: `http` = `desktop.port` (web client) and `agent` = 8081 (the in-pod agent's API for the
  gateway, section 7.7). The Service exposes both.
- Volumes:

| mount | volume |
| --- | --- |
| `storage.mountPath` (default `/persist`) | the PVC; the image owns the layout below it |
| `/run` | emptyDir `Memory`, `desktop.runSizeLimit` (default 256Mi) |
| `/dev/shm` | emptyDir `Memory`, `desktop.shmSize` (default 1Gi) |
| `/run/webtop/sa` | projected `serviceAccountToken` (audience `powerApi.audience`, 3600 s, path `token`) |
| `/dev/net/tun` | only with `desktop.tunDevice`: hostPath `/dev/net/tun`, type `CharDevice` |

Env (plus `desktop.extraEnv`):

| name | value |
| --- | --- |
| `WEBTOP_CONTROLLER_URL` | `powerApi.url` (no trailing slash) |
| `WEBTOP_TOKEN_PATH` | `/run/webtop/sa/token` |
| `WEBTOP_DESKTOP` | desktop name; the image's login name for the user (section 4) |
| `WEBTOP_USER` | original user ID (informational) |
| `WEBTOP_DISPLAY_NAME` | `status.displayName`, only when set: the user's GECOS (else the login name) |
| `WEBTOP_NAMESPACE` | namespace (informational) |
| `WEBTOP_HTTP_PORT` | `desktop.port` — the web client (Selkies) must listen here, plain HTTP |
| `WEBTOP_PERSIST_PATH` | `storage.mountPath` |

The image's `webtop-init` writes every `WEBTOP_*` variable to `/run/webtop/env`, since systemd units
do not inherit the container env. The controller and agent do not read the two informational ones;
they are there for desktop-side units and scripts. `webtop-init` adds `WEBTOP_LOGIN_NAME`,
`WEBTOP_LOGIN_GECOS` and `WEBTOP_HOME_PATHS` (space-separated), its decisions. The agent reports the
account's actual name (passwd entry of uid 1000) and the mounted home paths.

Optional Nix settings, never set by the controller (the chart sets them from `desktop.nix.*` through
`desktop.extraEnv`). `webtop-init` writes them to `/run/webtop/nix.conf`, which `/etc/nix/nix.conf`
includes; unset adds nothing:

| Variable | nix.conf setting |
| --- | --- |
| `WEBTOP_NIX_EXTRA_SUBSTITUTERS` | `extra-substituters`, space-separated URLs |
| `WEBTOP_NIX_EXTRA_TRUSTED_PUBLIC_KEYS` | `extra-trusted-public-keys`, space-separated `name:key` |

#### Selkies settings

The controller sets one variable per entry of `desktop.selkies`, plus the rendered `desktop.title`
as `SELKIES_UI_TITLE` (unless `desktop.selkies` has that key; then that literal value wins, untemplated).
`SELKIES_X` is passed as `WEBTOP_SELKIES_X`, sorted by name so the template hash is stable.
`webtop-init` writes them as `SELKIES_X="value"` to `/run/webtop/selkies.env` (root-owned, `0644`,
before systemd starts; also `SELKIES_PORT=$WEBTOP_HTTP_PORT`, last) and leaves them out of
`/run/webtop/env`. The image lists the file in `services.webtop.environmentFiles`.

- `desktop.title` is a Go `text/template`, parsed once from the config. Fields: `.User` (login),
  `.DisplayName` (the login for now), `.Desktop` (resource name). They are data: a name containing
  `{{ ... }}` stays literal.
- Values are one line. In the config, a key not matching `^SELKIES_[A-Z0-9_]+$`, `SELKIES_PORT`, or a
  value with a control character (CR, LF, tab, DEL, C1, U+2028/9), invalid UTF-8 or more than 256
  characters (or any Unicode format character, category Cf) is a config error. The rendered title is cleaned instead: format characters (bidi controls, zero-width, BOM) are dropped, control characters become
  spaces, it is cut to 256 characters.
- The file is written double-quoted with `\` and `"` escaped; systemd does no `$` expansion. `webtop-init`
  also skips a variable whose value has a control character or exceeds 1024 bytes.
- Selkies shows `SELKIES_UI_TITLE` as `document.title` and as text in its sidebar (React text, no
  HTML), and only when the variable is set: that is what makes the setting "overridden" for the client.
  Anything else that shows the title (a gateway page) must go through `html/template`.
- Precedence, low to high: the image (`extraEnvironment`) < the file above < the user's allowlisted
  `~/.config/webtop/selkies.env`. The last is a convenience, not a boundary: the user can edit their own
  systemd user units. Restrictions that matter (file transfers off) must not rely on it.

The software center's default (chart: `desktop.softwareCenter.unfreeListedDefault`, default true) is the
pod env `NSC_UNFREE_LISTED_DEFAULT`, `1` or `0`. `webtop-init` writes it, when it is one of those,
as `NSC_UNFREE_LISTED_DEFAULT=<v>` to `/run/webtop/session.env`, which the image adds to
`webtop-session.service` (so it reaches Plasma and the apps it starts). It only sets the default for
listing unfree packages: the user's choice in `~/.config/nix-software-center/sources.json` wins.

The cap on app screens (chart: `desktop.maxAppScreens`, default 4, 1 to 8) is the pod env
`WEBTOP_MAX_APP_SCREENS`, read by the agent (section 7.7). Each visible app screen costs a KWin output
plus a capture and an encoder.

#### Desktop defaults

The chart's `desktop.defaults` (`timeZone`, `locale`, `language`, `formats`, `xdgConfig`, `homeModule`) renders the
ConfigMap `<fullname>-desktop-defaults`, mounted read-only at `/run/webtop/defaults-source` in every desktop pod and
set as the image's `services.webtop.defaults.source`. Its keys are the entries of webtop-nix's defaults directory:
`session.conf` (the first four values), `xdg/<name>` (key `xdg-<name>`) and `home.nix`. What each entry means, how a
user overrides it and when it applies are webtop-nix's
([README, "Admin defaults"](https://github.com/lhns/webtop-nix#admin-defaults)). The chart adds a checksum of the data
to the pod annotations, so a change marks running desktops `updatePending` (ADR 0006) and reaches each at its next
start. `desktop.defaults.xdgConfig.plasma-localerc` is refused (webtop-locale owns that file); the removed
`desktop.timeZone` and `desktop.locale` fail the render. The ConfigMap is readable by every desktop user: no secrets.

Optional agent overrides, never set by the controller (the defaults are what ships):

| Variable | Default |
| --- | --- |
| `WEBTOP_POWER_HOOK_TIMEOUT` | `60s`: how long the power hook retries the request |
| `WEBTOP_IDLE_REASON_THRESHOLD` | `60s`: input idleness at poweroff that counts as `idle` |
| `WEBTOP_COMPOSITOR_TIMEOUT` | `3m`: how long KWin may stay non-compositing, once the target and the web port are up, before the agent reports the stuck stage `Compositor not active` |
| `WEBTOP_AGENT_STATE_DIR` | `/run/webtop`: must stay so, webtop-nix's Sleep and session stop write the state files there |
| `WEBTOP_AGENT_WAYLAND_SOCKET` | unset: the agent finds it from the user manager's `WAYLAND_DISPLAY` |
| `WEBTOP_AGENT_USER` | unset: the account with uid 1000 (renamed at boot) |
| `WEBTOP_SUSPEND_MODE` | read by webtop-nix's Sleep (`sleep.environmentFiles`); `freeze` (default): Sleep freezes the session; `poweroff` saves it and powers off (reason `suspend`) |

**Image obligations:**

- Serve the web client on `WEBTOP_HTTP_PORT`, and work under a path prefix: load assets and open the
  websocket relative to the page URL. The gateway serves admins at `/u/<name>/` (section 7.3).
- A poweroff from inside must end PID 1 with an exit code in `controller.cleanExitCodes`. The default `[0]` is not yet confirmed against a real
  container exit after a poweroff; set the list to the code the image actually exits with.
- Re-read the token file on every request (kubelet rotates it).
- Report the desktop user's login name and home paths (section 8.3).

## 6. Removed users and unseen desktops

kww has no user directory. Access is the gateway's check of `Remote-Groups` on every request
(section 7.1); a user removed at the provider is refused from their next request. Their desktop
keeps running until stopped:

- **By hand:** `desktops stop <user>` (reason `admin`) or `desktops remove <user>` (section 12).
- **Optionally by itself:** `desktop.stopAfterUnseen` (default `0s`, off; otherwise at least `24h`).
  A leader-only sweep (every 10 minutes) sets `powerState: Off` with reason `unseen` when
  `spec.lastSeen` is older than that. Connected clients do not exempt a desktop (an open tab of a
  removed user is the case). Data is kept; a permitted user starts it again. A desktop without
  `spec.lastSeen` (existing ones after an upgrade) gets it set on the first sweep and is not expired
  then. The sweep stops nothing while the gateway Deployment (`controller.gatewayDeployment`, set by
  the chart) has no ready pod, so a gateway outage cannot read as absent users. A login-provider
  outage longer than the value cannot be told apart: keep it well above any outage.

`spec.displayName` is the owner's name from `gateway.displayNameHeader` (default `Remote-Name`).

## 7. Gateway

### 7.1 Identity

- User: header `gateway.userHeader` (default `Remote-User`). Missing → 401.
- Groups: header `gateway.groupsHeader` (default `Remote-Groups`), split on
  `gateway.groupsSeparator` (default `,`), trimmed.
- `gateway.requiredGroup` (optional): users not in it get 403 and nothing is created. Members of `adminGroup` pass it.
- `gateway.adminGroup` (optional): may open any desktop.
- **Last seen and name.** For a request that passed these checks and belongs to the desktop's own
  user (not an admin looking at another's), the gateway merge-patches `spec.lastSeen` (at most
  hourly) and `spec.displayName` when `gateway.displayNameHeader` carries a different name (after
  cleaning, as for `status.displayName`; header text is untrusted and never rendered in a page).
  A missing header leaves the stored name. A new Desktop is created with both. Refused requests
  write nothing. An open tab pings every minute, so a legitimate open tab keeps `lastSeen` fresh.
- `gateway.trustedProxyCIDRs` (optional): if set, requests whose remote address is outside get 403.
  The gateway does not use `X-Forwarded-For` to decide this.

### 7.2 Paths

| path | handled |
| --- | --- |
| `/u/<user>/…` | desktop `sanitize(<user>)`; allowed for that desktop's own user and admins. Unknown → 404; admins never create desktops |
| everything else | the caller's own desktop |
| `<base>/.webtop/events` | SSE, never proxied |
| `<base>/.webtop/logout` | 303 to `gateway.logoutUrl` for the desktop's own user (section 7.8); 404 without a URL, 403 for anyone else or a cross-site request |
| `<base>/.webtop/reload.js` | the script injected into the desktop's page (section 7.3), never proxied |
| `<base>/.webtop/ping` | GET/HEAD, `204` `no-store`, same authz as the desktop, never proxied. `reload.js` requests it every 60 s so the forward-auth in front of the gateway sees traffic and keeps its session active while a tab is open |
| `<base>/.webtop/start` | POST start, never proxied |
| `<base>/.webtop/focus?screen=<n\|main>` | POST, `204` `no-store`, same authz as the desktop, same-origin like start (section 7.5), never proxied, never starts or wakes: `503` unless `Running`, `400` unless `screen` matches `^[0-9]{1,9}$\|^main$`. Relays to the agent's `POST /api/v1/focus/<screen>` (section 7.7); the agent's `404` (no app on that screen) is passed on, other failures are `502`. `reload.js` calls it |
| `<base>/.webtop/wake` | POST wake from the sleep page (section 7.5), never proxied |
| `<base>/.webtop/restart`, `<base>/.webtop/stop` | POST from the error page (section 7.5), never proxied |
| `<base>/.webtop/apps/<id>/icon` | GET/HEAD, same authz as the desktop, never proxied: the app's icon from the agent (section 7.6) |
| `<base>/apps/` | top-level `GET` only: the apps overview (section 7.6). Any other request to it is proxied |
| `<base>/apps/<name>` | top-level `GET` only: single-app streaming (section 7.6). The gateway owns this path for navigations; any other request to it is proxied like every other path |

`/u/<user>` without the trailing slash redirects (308) to `/u/<user>/`. Paths under `/u/` never
reach a desktop unprefixed. A non-admin asking for someone else's desktop gets 403 whether or not it
exists.

`<base>` is `/u/<user>` for the prefixed form and empty otherwise. **Nothing under
`<base>/.webtop/` is ever proxied**, so a desktop cannot spoof the gateway's pages.

### 7.3 Proxying

When the desktop's phase is `Running`, requests go to `status.url` with
`httputil.ReverseProxy` (`FlushInterval: -1`, websockets via `Upgrade`). For `/u/<user>/…` the
prefix is stripped and `X-Forwarded-Prefix: /u/<user>` is set. `X-Forwarded-Host`,
`X-Forwarded-Proto` and `X-Forwarded-For` are set; incoming values are kept (For: appended) only
from a peer in `trustedProxyCIDRs`, and an incoming `X-Forwarded-Prefix` is always dropped. The user and groups headers are passed through.
Desktop state comes from a `Desktop` informer; the gateway reads no core resources.

**Reload script.** In the response to a navigation (section 7.4) `GET` that is `200` with
`Content-Type: text/html`, no `Content-Encoding` and at most 4 MiB, the gateway inserts
`<script src="<base>/.webtop/reload.js" defer></script>` before `</head>` (else before
`</body>`; neither → unchanged), sets `Content-Length` and drops `ETag` and `Last-Modified`. For
navigations `Accept-Encoding` is not forwarded, so the transport's own gzip is decoded and the page
arrives readable; a response that is encoded anyway is left alone. The desktop's
`Content-Security-Policy` is not changed: a policy that blocks the script or `connect-src 'self'`
would stop the reload (Selkies sends only `frame-ancestors`). The script opens `<base>/.webtop/events?watch=1` and reloads the
tab when the desktop leaves `Running` or `podUID` changes after the tab saw `Running`; it reconnects
with backoff (1 s to 30 s) and never reloads for a state it did not see go away.

### 7.4 Navigation

A request is a **navigation** iff:

- `Sec-Fetch-Mode` present: it is `navigate` and `Sec-Fetch-Dest` is `document` or absent;
- otherwise: method `GET`, `Accept` contains `text/html`, and no `Upgrade` header.

### 7.5 Desktop not running

| state | navigation | anything else |
| --- | --- | --- |
| no Desktop (own path) | create with `powerState: On` → boot page | 503 |
| `Off`, no `status.lastStop` | patch `powerState: On` → boot page | 503 |
| `Off`, `status.lastStop` set | **off page** "desktop is off — start it?" | 503 |
| `On`, not `Running`, not `Error` | boot page | 503 |
| `On`, phase `Error` | **error page** "Your desktop couldn't start" (boot page for 30 s after Restart) | 503 |

503 responses carry `Retry-After: 5` and never start anything. Boot page, off page, SSE and all
gateway error pages send `Cache-Control: no-store`.

**Boot page:** HTML that opens `EventSource("<base>/.webtop/events")` and shows `stage`,
`bootProgress` and `message`; on the `ready` event it reloads the page.

**SSE** `GET <base>/.webtop/events` (same authz as the desktop, never starts it):

```
event: status
data: {"phase":"Starting","stage":"Booting","bootProgress":40,"message":""}

event: ready
data: {}
```

`status` on connect and on every change; a `: ping` comment every 15 s; `ready` once phase is
`Running`, then the stream ends (unless the request has `?watch=1`: then no `ready`, the stream stays
open). `podUID` is added when set. While the stage is `PullingImage` and the pull has a start time,
`status` carries `"pull": {"elapsedSeconds", "sizeBytes"?, "lastDurationSeconds"?}`; `elapsedSeconds`
is computed by the gateway, and the boot page ticks it locally and shows "about N%" (elapsed /
last duration, at most 95) as an estimate, only when `lastDurationSeconds` is known. Stage texts:
`CreatingContainer` "Creating container", `PullingImage` "Downloading image", `AttachingVolume`
"Waiting for volume", `StoppingPrevious` "Waiting for the previous session to stop".

**Off page / start:** a form `POST <base>/.webtop/start` with field `csrf`. CSRF is a double-submit
cookie: the page sets cookie `webtop_csrf` (random 32 bytes, base64url, `HttpOnly`, `SameSite=Strict`,
`Path=/`, `Secure` unless `gateway.cookieSecure=false`) and the same value in the form. The POST is
accepted only if field = cookie (constant-time) and the request is same-origin (`Sec-Fetch-Site:
same-origin` if present, else `Origin` host = `Host`; neither → reject). Success: patch
`powerState: On`, `303` to `<base>/`. Failure: 403.

**Error page / restart and stop:** for phase `Error` the page gives the cause in plain words by stage
(`ErrImagePull`/`ImagePullBackOff`/`InvalidImageName` "The desktop image couldn't be downloaded.",
`CrashLoopBackOff` "The desktop keeps crashing while it starts.", `CreateContainerConfigError`
"The desktop's container couldn't be set up.", `Error` "Your desktop's name or storage couldn't be set up. An
administrator has to fix this.", anything else "The desktop's container failed to start."). `status.message`, pod and node names never
reach it. Except for stage `Error` it has two forms with the off page's CSRF cookie and field, `POST <base>/.webtop/restart`
(patch `spec.restartRequestedAt=now`; the controller then applies the template as for a reboot, section 3.2 step 4) and
`POST <base>/.webtop/stop` (patch `powerState: Off`; the stop is recorded as `error`, and the response is the off page). Same authz as
start; both do nothing but `303` to `<base>/` unless the desktop is `On` and in a recoverable `Error`. They are counted as outcomes
`restart` and `stop`, the page as `failed`, and a Restart as start trigger `restart`. The page follows the SSE stream and reloads
when the phase leaves `Error`; the boot page reloads when it turns to `Error`, and its events carry no `message` then.

**Sleep page / wake:** Sleep (ADR 0019) freezes the stream, so a tab would stay on the last frame.
`status` carries `"asleep": true` while `status.asleep` is set (omitted otherwise), and `reload.js`
reloads a tab that saw the desktop awake when it falls asleep (a tab opened while it is still waking
does not). A navigation (section 7.4) `GET` to a `Running` desktop with `status.asleep` gets the
**sleep page** "Your desktop is asleep" instead of being proxied; it has the off page's CSRF cookie,
and a form `POST <base>/.webtop/wake` with fields `csrf` and optionally `app` (as for start). Any other
request (websocket, XHR, a frame) is proxied as usual. `/apps/<name>` and `/apps/` navigations get the same page (the form carries `app`, `*` for the overview).

**Reload and app pages:** a tab on an app page (fragment `#display<n>-right-<app>`, section 7.6) goes
back through `<base>/apps/<app>` (`location.replace`) instead of reloading in place wherever
`reload.js` would reload, so the app is placed again after a desktop restart. The same happens when
the focus call answers `404`. It also sets the tab's title to the app id.

Wake checks CSRF like start, sets cookie `webtop_wake=1` (`HttpOnly`, `SameSite=Lax`, `Path=<base>/`,
`Max-Age=60`, `Secure` as above) and answers `303` to `<base>/` or `<base>/apps/<app>`. A navigation
with that cookie is proxied while the desktop is asleep: it opens a new Selkies session, which makes
the desktop thaw the session. A navigation with the cookie to an awake desktop expires it, so the next
Sleep shows the page again.

### 7.6 Single app: `<base>/apps/<name>`

`<name>` is a desktop file id (`org.kde.dolphin`, `firefox`), `[A-Za-z0-9._-]+`; any application
installed for the desktop's user is allowed. A path that does not match, or a request that is not a
navigation (section 7.4) `GET`, is not this route and is handled as any other path.

| desktop | result |
| --- | --- |
| not `Running` | the table of section 7.5; the boot page reloads the same URL, and the off page's start form carries `app=<name>` (validated against the name rule) so the `303` goes to `<base>/apps/<name>` instead of `<base>/` |
| `Running` | the gateway calls the agent (section 7.7); on success `303` to `<base>/#display<n>-right-<name>` (`no-store`), `<n>` being the agent's display number |
| agent: too many app screens (`409`) | 409 page "Too many apps open": the desktop shows at most `max` apps at once, close one of their tabs and try again, with the open app ids (validated against the name rule, dropped otherwise) and a link to `<base>/apps/`; counted as outcome `capped` |
| agent: unknown application | 404 page "App not found" |
| agent unreachable or failed | 503 page with `Retry-After: 5` |

**Overview: `<base>/apps/`** (exactly this path, a navigation `GET`, like `<base>/apps/<name>`):

| desktop | result |
| --- | --- |
| not `Running` | the table of section 7.5; the off page's start form carries `app=*` (a value that is no app name) so the `303` goes to `<base>/apps/` |
| `Running` | the gateway lists the apps through the agent (section 7.7) and renders a page: a grid of icon and name, a "running" badge, each linking to `<base>/apps/<id>` with `target="webtop-app-<id>"` (one named tab per app); `no-store`, counted as outcome `apps` |
| agent unreachable or failed | 503 page with `Retry-After: 5` |

The icons are `<img>` of `<base>/.webtop/apps/<id>/icon`, which relays `GET /api/v1/apps/<id>/icon`
of the agent: only `image/png` and `image/svg+xml` pass (else 502), `Content-Type` and `Cache-Control`
come from the agent, and every answer has `X-Content-Type-Options: nosniff`. An SVG also gets
`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`, since SVG can carry script.
A desktop that is not `Running` answers 503, a missing icon 404. An app without an icon shows its
initial.

The target is the Selkies page with the fragment `#display<n>-right`: it makes KWin grow the screen
`SELKIES-<n>` and streams only that screen, with the app fullscreen on it ([webtop-nix, "One app per
screen"](https://github.com/lhns/webtop-nix#one-app-per-screen)). The `-<name>` suffix is ignored by
Selkies and tells `reload.js` which app the tab shows. It is the normal page, so the reload script
(section 7.3) is injected as usual. Every app has its own screen and tab. Limits: the cap (section
7.7) is enforced only where the agent is asked, so a reload or a restored tab bypasses it; a second
page of the same `<n>` takes over and the first shows "Connection Terminated"; audio plays on the main
page only; placement does not survive a desktop restart (the tab then goes back through
`/apps/<name>`, section 7.5).

### 7.7 Agent API (gateway to agent)

The agent listens on `:8081`, plain HTTP, started independently of the
controller API. The gateway reaches it at the host of `status.url` with port 8081.

**No authentication of its own** (ADR 0010): the desktop NetworkPolicy admits only gateway pods to
the desktop's ports, the same boundary that protects the web client on `desktop.port`. The gateway
alone maps the authenticated user (or admin) to a desktop. With `networkPolicy.enabled=false` the
cluster must provide that isolation itself.

`POST /api/v1/apps/<name>` (no body):

1. `<name>` must match `[A-Za-z0-9._-]+` (else 400) and `applications/<name>.desktop` must exist in
   the user's data dirs (`~/.local/share`, the user manager's `XDG_DATA_DIRS`, the NixOS profiles)
   (else 404).
2. The app's display number is sticky: `/run/webtop/app-screens` (JSON) maps each app to a number,
   starting at 3 (2 is Selkies' own sidebar button), and a number is never reused while the file
   lives, so a reload or restored tab cannot reach another app.
3. An app without a live screen needs a free one: the live screens (`webtop-app-screen screens`, one
   `SELKIES-<n>` per line; failure: 502) plus the screens opened less than 60 s ago whose page may not
   have connected yet are counted, and at `WEBTOP_MAX_APP_SCREENS` (default 4; unset or invalid: 4) the
   call answers `409` `{"max":<cap>,"open":["<id>",...]}` and starts nothing. An app that has a live
   or just-opened screen re-attaches and is never refused.
4. `webtop-app-screen place <name> SELKIES-<n>`, before the app starts, so its window lands on the
   screen as it opens. It is repeated on every call (harmless; it also recovers a lost KWin script).
   Failure: 502.
5. Unless an active unit `app-<name>@....service` or `app-<name>-<pid>.scope` exists in the user
   manager (how KDE/systemd name a launched app), start it:
   `systemd-run --user --collect -- kioclient exec <path of the .desktop file>`. Failure: 500.
   `200` `{"display":<n>}` on success.

Reloading `/apps/<name>` therefore does not start a second instance while the app runs; an app that
is not recognised as running but has its own single-instance logic just raises its window.

`GET /api/v1/apps` answers `200` with a JSON array `[{"id","name","icon","running"}]` sorted by name
(`[]` if none):

- Sources: `applications/*.desktop` (top level only; a file in a subdirectory has the id
  `dir-file` and is not listed) in the same data dirs as above. The first dir with an id wins, even
  if its entry is hidden. Skipped: `Type` other than `Application`, `NoDisplay=true`, `Hidden=true`,
  `OnlyShowIn` without `KDE`, `NotShowIn` with `KDE`, ids that do not match the name rule.
- `name` is the unlocalized `Name`. `running` is the active-unit check of step 5 above, from one
  query of the user manager (if it fails, nothing is marked running). `icon` is true if the icon
  below resolves.

`GET /api/v1/apps/<name>/icon` answers with the image (`image/png` or `image/svg+xml`,
`Cache-Control: max-age=3600`, `nosniff`), 404 if the app or its icon does not exist. `Icon=` is an
absolute path (served only if it is inside one of the data dirs) or a name, searched as
`<datadir>/icons/{breeze,hicolor}/{scalable,256x256,128x128,64x64,48x48}/apps/<icon>.{svg,png}`, then
`<datadir>/pixmaps/<icon>.{svg,png}`. Files over 1 MiB, empty files and other formats (XPM, SVGZ) are
ignored.

The agent runs as root and `~/.local/share` is the user's, so both endpoints read a desktop or icon
file only if, with symlinks resolved, it is a regular file in the Nix store (`/nix/store`) or inside
the user's own `~/.local/share` (itself resolved inside the home). A link leading anywhere else is
ignored.

`POST /api/v1/focus/<screen>` raises a screen: `main` or a number matching `^[0-9]{1,9}$` (else 400)
that is in the app-screens file (else 404), by `webtop-app-screen focus SELKIES-<n>` or `focus main`.
`204`; failure 502.

`GET /healthz` answers `ok`.

### 7.8 Logout

With `gateway.logoutUrl` set, a session that logs out signs the browser out at the login provider.
Without it nothing is redirected and the session comes back.

1. The image runs `webtop-agent session-exit` as `ExecStopPost` of the session unit. It writes
   `/run/webtop/logout` (time and the user's choice) when the unit ended with result `success` and
   no `poweroff`, `reboot` or `halt` job is queued (those are shutdowns, reported by the power hook).
2. The user's choice is `onLogout=true|false` in `~/.config/webtop/session.conf`, read at that
   moment by the root agent: only that key, only plain booleans, nothing else; links are resolved and
   followed only into `/nix/store` (home-manager); any other link, a non-regular file or one over
   4 KiB counts as no file. No URL can be set there.
3. The steady report carries `loggedOutAt` and `logoutPref` (section 8.3); the controller stores
   `status.lastLogout` (reset with the pod) if the choice, or `gateway.ssoLogoutDefaults.onLogout`
   without one, is true and `logoutUrl` is set.
4. The SSE status event carries `loggedOut` for the desktop's own user only, and only with a
   `logoutUrl`. `reload.js` remembers the first value it sees and acts only on a later change, by
   navigating to `<base>/.webtop/logout`. The URL is never in a page or an event.

The session unit must restart after a logout (webtop-nix); the browser lands in the same Plasma
session when the user signs in again.

### 7.9 Lock

Lock does not lock or end the session: it signs the browser out at the login provider, like a logout
that leaves the session running. It needs `gateway.lockLogsOut` and `gateway.logoutUrl`; off, Lock is
hidden and blocked.

1. The chart passes `WEBTOP_LOCK_SCREEN=true|false` (from `gateway.lockLogsOut`) in the pod env; the image lists
   `/run/webtop/env` in `services.webtop.lockEnvironmentFiles`, so Plasma shows Lock or not at each session start.
2. The lock itself is webtop-nix's ([README, "Lock screen"](https://github.com/lhns/webtop-nix#lock-screen), its ADR 0010): it never stays, there is
   never a greeter, and the session and its processes are untouched. It leaves the marker
   `/run/user/<uid>/webtop/lock`, the interface to the agent.
3. The agent reads the marker (a regular file of at most 64 bytes, no link followed, only an RFC 3339
   time) and `onLock=true|false` of `~/.config/webtop/session.conf` (read as in 7.8), and reports
   `lockedAt` and `lockPref` (section 8.3). The controller stores `status.lastLock` (reset with the pod)
   if the choice, or `gateway.ssoLogoutDefaults.onLock` without one, is true, `gateway.lockLogsOut` is on
   and `logoutUrl` is set. So for a user with `onLock=false`, Lock clears itself and nothing else happens.
4. The SSE status event carries `locked` for the desktop's own user only, and only with a `logoutUrl`.
   `reload.js` acts on a change after it connected, never on the first value, by navigating to
   `<base>/.webtop/logout`, as for a logout.

## 8. Power/report API (controller, every replica)

Listens on `powerApi.addr` (default `:8082`), plain HTTP inside the cluster. All requests:
`Authorization: Bearer <contents of WEBTOP_TOKEN_PATH>`, `Content-Type: application/json`.

### 8.1 Authentication

1. TokenReview with `spec.audiences = [powerApi.audience]`.
2. Require `status.authenticated`, `status.audiences ∋ powerApi.audience`, username
   `system:serviceaccount:<namespace>:<desktop.serviceAccountName>`.
3. Pod from `user.extra["authentication.kubernetes.io/pod-name"]` and `…/pod-uid`; the live pod of
   that name must have that UID (stale tokens of deleted pods fail).
4. The pod must carry `app.kubernetes.io/managed-by=kube-webtop-workspaces` and
   `webtop.lhns.github.io/desktop=<d>`; Desktop `<d>` is the only one it can act on.

Missing/invalid token → 401. Valid token but checks 2–4 fail → 403. TokenReview call failure → 503.

### 8.2 `POST /api/v1/power`

```json
{"action": "stop|suspend|reboot", "reason": "user|idle|suspend|kube-stop"}
```

`reason` may be omitted for `reboot`. `unseen`, `exit`, `admin`, `error` → 400. Effects, **completed before the response**:

| action / reason | effect |
| --- | --- |
| any action, `kube-stop` | none (the pod is already being deleted; restart/drain/scale is someone else's call) |
| `stop`, `suspend` | `status.lastStop={now, reason}` then `spec.powerState=Off`. Idempotent: if already `Off` with `lastStop.time ≥ lastStart`, nothing changes |
| `reboot` | `spec.restartRequestedAt=now`, unless it is already newer than the pod's start time |

The leader then scales the Deployment to 0 (or rolls it out). The handler never touches the
Deployment. Response `200 {"desktop":"<d>","action":"…","reason":"…"}`.

**Ack semantics:** the agent's hook blocks the shutdown until it gets the 200 (or its own timeout),
so the stop is recorded before PID 1 exits and the exit is never mistaken for a crash or an
`exit` stop. Retrying is safe.

### 8.3 `POST /api/v1/report`

All fields optional; absent fields are left unchanged:

```json
{"stage": "Starting Plasma", "bootProgress": 60, "ready": false,
 "connectedClients": 1, "idleSeconds": 12, "asleep": false,
 "loginName": "alice", "homePaths": ["/home/alice", "/home/user"],
 "loggedOutAt": "2026-01-01T12:30:00Z", "logoutPref": true,
 "lockedAt": "2026-01-01T12:31:00Z", "lockPref": true}
```

`loginName` must be a valid login name (section 4) and `homePaths` at most 16 entries of
`/home/<login name>` (else `400`); they set `status.loginName` and `status.homePaths`. A change of
`loginName` away from the desktop name (or back) also records an Event on the Desktop
(`LoginNameFallback` warning / `LoginName`). `loginError` (at most 512 characters, sent with
`loginName`; empty clears) is why `usermod` failed at activation; it sets `status.loginNameError`.
`softwareRestore` (`active`, `done`, `total`, `failed`; counts at most 100000) is the restore's progress
(webtop-nix's `webtop-restore-progress`, which the agent reads from `/run/webtop/restore/progress.json`, or active
while `/persist/nix/reinstall/pending` exists); it sets `status.softwareRestore`. The agent also shows it as a
KDE job (`webtop-restore-job`).

`loggedOutAt` (RFC 3339, else `400`) is the session's logout marker (section 7.8) and `logoutPref` the
user's own choice, absent if none. The controller sets `status.lastLogout` only when the choice, or
`gateway.ssoLogoutDefaults.onLogout` without one, is true and `gateway.logoutUrl` is set.
`lockedAt` (RFC 3339, else `400`) and `lockPref` work the same for `status.lastLock` (section 7.9), which
also needs `gateway.lockLogsOut`.

Reports from a pod whose UID ≠ `status.podUID` → `409`, ignored; right after a pod starts the
controller may not have recorded it yet, so the agent retries a 409 (every few seconds). `ready: true` sets `agentReady`.
`stage`/`bootProgress` are only applied before ready. `stage` is free text for the boot page, at
most 64 characters (longer → `400`); it never becomes a metric label value (section 11). Response `204`.

`notices: true` asks for the desktop's `status.updatePending` in the answer: the response is then `200
{"updatePending": true|false}` instead of `204`. The agent sends it with every report and keeps
`/run/webtop/notices/update-pending.json` (webtop-nix's notice channel, its ADR 0019) in step: written while
true, removed while false; a controller that answers `204` leaves it as it is. The notice reads "Updates are
ready" / "Restart your desktop to apply them." with the `restart` action. The value is the one the
reconciler last stored, so a change reaches the desktop with a report after it
(every 30 s) ([ADR 0021](adr/0021-update-pending-is-a-notice-file-in-webtop-nixs-channel.md)).

**Readiness:** the agent reports `ready: true` only when `multi-user.target` and the user's `plasma-workspace.target` are active, the web port accepts connections, and KWin is compositing (D-Bus `org.kde.KWin` `/Compositor` `org.kde.kwin.Compositing.active` on the user bus; any compositing type counts). While KWin is not compositing the stage is `Starting compositor`; after `WEBTOP_COMPOSITOR_TIMEOUT` it becomes `Compositor not active` (the agent keeps polling and still turns ready if KWin recovers).

**Agent cadence:** during boot on every stage change (≤ 1/s); after ready every 30 s, and
immediately when `connectedClients` or `asleep` changes. `asleep` is also sent once when Sleep starts and ends.

### 8.4 Other

`GET /healthz` on `powerApi.addr` → 200, no auth.

## 9. Configuration

One YAML file, `--config` (default `/etc/kube-webtop-workspaces/config.yaml`), shared by both
modes; the chart renders it from its values. Flags: `--mode=gateway|controller` (required),
`--config`, `--kubeconfig`, `--log-level` (`info`), `--log-format` (`json`|`text`).

```yaml
namespace: ""                     # default: POD_NAMESPACE env, then the in-cluster namespace
names:
  prefix: desktop-
metricsAddr: ":9090"
healthAddr: ":8081"

controller:
  leaderElection: true
  leaderElectionID: kube-webtop-workspaces
  requireAgentReady: true
  cleanExitCodes: [0]
  maxConcurrentReconciles: 4      # Desktops reconciled in parallel
  gatewayDeployment: ""           # the gateway Deployment; required with desktop.stopAfterUnseen (the chart sets it)

powerApi:
  addr: ":8082"
  audience: kube-webtop-workspaces
  url: ""                         # required: http://<controller-svc>.<ns>.svc:8082

desktop:
  image: ""                       # required
  stopAfterUnseen: 0s             # 0s = never; else at least 24h (section 6)
  imagePullPolicy: IfNotPresent
  imagePullSecrets: []            # [{name: ...}]
  port: 8080
  serviceAccountName: webtop-desktop
  runtimeClassName: ""
  priorityClassName: ""
  hostUsers: false
  resources: {}
  nodeSelector: {}
  tolerations: []
  affinity: {}
  podLabels: {}
  podAnnotations: {}
  extraEnv: []                    # []corev1.EnvVar
  selkies: {}                     # SELKIES_* -> string, see "Selkies settings"
  title: ""                       # text/template, see "Selkies settings"; the chart defaults it
  extraVolumes: []                # []corev1.Volume
  extraVolumeMounts: []           # []corev1.VolumeMount
  tunDevice: false                # mount the node's /dev/net/tun
  seccompProfile: null            # corev1.SeccompProfile of the default securityContext; null: RuntimeDefault
  securityContext: null           # replaces the default container securityContext
  shmSize: 1Gi
  runSizeLimit: 256Mi
  terminationGracePeriodSeconds: 420

storage:
  storageClassName: ""            # empty: cluster default
  size: 50Gi
  accessModes: [ReadWriteOnce]
  mountPath: /persist
  labels: {}
  annotations: {}

gateway:
  addr: ":8080"
  userHeader: Remote-User
  groupsHeader: Remote-Groups
  groupsSeparator: ","
  adminGroup: ""
  requiredGroup: ""
  trustedProxyCIDRs: []          # optional; empty = any peer trusted, desktop sees X-Forwarded-Proto: http
  cookieSecure: true
  displayNameHeader: Remote-Name  # empty header keeps the last name
  logoutUrl: ""                   # absolute http(s) URL the browser goes to on logout; empty: nowhere
  ssoLogoutDefaults: {onLogout: true, onLock: true}
  lockLogsOut: false              # offer Lock; it signs out at the provider, the session keeps running; needs logoutUrl
```

Ports: gateway HTTP `:8080`, controller power API `:8082`, metrics `:9090`, probes `:8081`
(`/healthz`, `/readyz`). The desktop pod has its own `:8081` for the agent API (section 7.7). Leader election uses a `coordination.k8s.io` Lease.

## 10. RBAC

Generated by controller-gen into `config/rbac/controller/role.yaml` and
`config/rbac/gateway/role.yaml` (as ClusterRoles, the only kind it emits). The chart renders the
namespaced rules as a Role + RoleBinding in the release namespace and only `tokenreviews` as a
ClusterRole + ClusterRoleBinding.

- **gateway** (Role): `desktops` get/list/watch/create/patch. Nothing else.
- **controller** (Role): `desktops` get/list/watch/update/patch/delete (delete: CLI `remove` only); `desktops/status` get/update/patch;
  `deployments`, `services` get/list/watch/create/update/patch; `persistentvolumeclaims`
  get/list/watch/create/patch (**no delete**); `pods` get/list/watch; `events` create/patch/list/watch;
  `leases` get/list/watch/create/update/patch.
  (ClusterRole): `tokenreviews` create.
- **desktop** ServiceAccount: no RBAC at all; its token is used only for the power API audience.

Neither role reads Secrets or ConfigMaps: the config file is a mounted file.

Network (besides DNS):

| component | egress | ingress |
| --- | --- | --- |
| gateway | API server; desktop Services on `desktop.port` and 8081 (agent) | ingress controller on `gateway.addr` |
| controller | API server | desktop pods on `powerApi.addr` |
| desktop | controller on `powerApi.addr` (plus whatever the chart allows) | gateway on `desktop.port` and 8081 (agent) |

Metrics and probes listen on `metricsAddr` and `healthAddr`. Desktop pods must not reach the gateway:
it trusts the user header (section 7.1).

## 11. Metrics

Prefix `webtop_`. Every replica exports its own counters. Per-desktop gauges come from the informer
cache and are exported **by the controller leader only** (non-leaders export none), so there are no
duplicate series.

Controller:

| metric | type | labels |
| --- | --- | --- |
| `webtop_desktop_info` | gauge (1) | `desktop`, `user`, `phase`, `stage` (section 3.4's stages; an agent's stage is reported as `Booting`) |
| `webtop_desktop_power_on` | gauge | `desktop` |
| `webtop_desktop_running` | gauge | `desktop` |
| `webtop_desktop_last_seen_timestamp_seconds` | gauge | `desktop` |
| `webtop_desktop_update_pending` | gauge | `desktop` |
| `webtop_desktop_boot_progress` | gauge | `desktop` |
| `webtop_desktop_connected_clients` | gauge | `desktop` |
| `webtop_desktop_idle_seconds` | gauge | `desktop` |
| `webtop_desktop_asleep` | gauge | `desktop` |
| `webtop_desktop_last_stop_timestamp_seconds` | gauge | `desktop`, `reason` |
| `webtop_desktop_stops_total` | counter | `reason` |
| `webtop_desktop_start_duration_seconds` | histogram | — (powerState On → Running) |
| `webtop_power_requests_total` | counter | `action`, `reason`, `code` |
| `webtop_report_requests_total` | counter | `code` |

Gateway:

| metric | type | labels |
| --- | --- | --- |
| `webtop_gateway_requests_total` | counter | `outcome` (`proxy`, `boot`, `off`, `start`, `asleep`, `wake`, `app`, `capped`, `unavailable`, `denied`, `notfound`, `error`) |
| `webtop_gateway_websockets` | gauge | — |
| `webtop_gateway_starts_total` | counter | `trigger` (`create`, `navigate`, `button`) |

Plus the controller-runtime and Go/process defaults.

## 12. Admin CLI

`kubectl -n <ns> exec deploy/<release>-controller -- /kube-webtop-workspaces desktops <command>`; it
uses the controller's in-cluster credentials, so it adds no authentication surface.

| command | effect |
| --- | --- |
| `list` | user, power, phase, last seen, clients, PVC |
| `stop <user>` | `powerState: Off`, reason `admin`; data kept |
| `remove <user>` | deletes the Desktop (Deployment and Service follow); the PVC stays and its name is printed. A user who is still permitted gets a new desktop on the next visit, which reuses the PVC |

There is no command that deletes data: the code has no PVC delete (section 3.1), and deleting a
dynamically provisioned volume may destroy it for good. That stays a deliberate manual step.
