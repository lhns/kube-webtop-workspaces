# kube-webtop-workspaces contract

The interface between the controller/gateway binary, the desktop image (and its in-pod agent) and
the Helm chart. Anything not stated here is an implementation detail and may change.

Version: `v1alpha1`. Go module: `github.com/lhns/kube-webtop-workspaces`.

## 1. Components

| component | binary | replicas | job |
| --- | --- | --- | --- |
| gateway | `kube-webtop-workspaces --mode=gateway` | 2+, stateless | auth-header routing, start on navigation, boot/off pages, reverse proxy |
| controller | `kube-webtop-workspaces --mode=controller` | 2+, leader election | reconcile `Desktop`s, LDAP sync (leader only); power/report API (every replica) |
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
| `user` | string, required, immutable (CEL `self == oldSelf`) | the user ID exactly as the auth header / LDAP has it |
| `powerState` | `On` \| `Off`, default `Off` | desired state |
| `restartRequestedAt` | RFC 3339 time, optional | set to request a restart ("reboot"); applies pending updates |

### status

| field | type | meaning |
| --- | --- | --- |
| `phase` | `Off` \| `Starting` \| `Running` \| `Stopping` \| `Error` | summary; the gateway proxies only in `Running` |
| `stage` | string | human-readable current step (section 3.4) |
| `bootProgress` | int 0–100 | 0 when off |
| `message` | string | detail for `Error` or an unauthorized desktop |
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
| `imagePull` | `{image, startedAt?, lastSizeBytes?, lastDurationSeconds?}` | the desktop image's pulls, for the boot page's download estimate (section 3.4); kept while off |
| `observedGeneration` | int | |
| `conditions` | `[]metav1.Condition` | `Authorized`, `Ready` |

Conditions:

- `Authorized`: `True` (reason `Member` or `LDAPDisabled`), `False` (`NotMember`), `Unknown`
  (`LDAPError` — no successful lookup yet; `InvalidName` — name ≠ `sanitize(spec.user)`).
- `Ready`: `True` iff phase `Running`.

## 3. Controller behaviour

### 3.1 Created objects

| object | name | owner | notes |
| --- | --- | --- | --- |
| Deployment | `<prefix><desktop>` | Desktop (controller ownerReference) | replicas 0/1, strategy `Recreate` |
| Service | `<prefix><desktop>` | Desktop (controller ownerReference) | ClusterIP, port `desktop.port`, selects the desktop label |
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
2. **Authorize:** `Authorized` from the LDAP member cache (section 6). On a start (`On` while the
   Deployment is at 0) of a user not in the cache, query LDAP directly for that user. Not authorized
   → no start, phase `Off`, `message` set. An LDAP error never stops a running desktop.
3. **PVC** when starting (section 3.1).
4. **Service** and **Deployment** (always, once valid).
   - Desired pod template rendered from the config (section 5); hash = SHA-256 of the rendered pod
     template without the `restarted-at` annotation (replicas excluded).
   - Template written when the Deployment is at replicas 0, when it goes 0→1, or when
     `spec.restartRequestedAt > status.observedRestartAt` (rollout; also stamps `restarted-at`).
   - Otherwise only `spec.replicas` is patched; a differing hash sets `status.updatePending=true`.
     **A running desktop is never restarted by an update.**
5. **Replicas:** 1 iff `powerState=On` and authorized, else 0.
6. **Stop recording:** when the controller scales 1→0 and `status.lastStop.time < status.lastStart`
   (nobody recorded a reason), it records reason `admin`. LDAP removal records `ldap`.
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
| `suspend` | agent (suspend mapped to save + poweroff) |
| `kube-stop` | agent (PID 1 got the stop signal from a pod deletion); never recorded |
| `exit` | controller (clean exit without a recorded stop) |
| `admin` | controller (`powerState` set `Off` by someone else) |
| `ldap` | controller (user left the group) |

### 3.4 Phase and stage

| condition | phase | stage |
| --- | --- | --- |
| `Off`, no pod | `Off` | `Stopped` |
| `Off`, pod exists | `Stopping` | `Stopping` |
| `On`, no pod yet | `Starting` | `Scheduling` |
| pod unschedulable | `Starting` | `Scheduling` (+ `message`) |
| container waiting (`ContainerCreating`), a `Pulling` event of the pod has no later `Pulled` | `Starting` | `PullingImage` |
| container waiting, a `FailedAttachVolume` event has no later `SuccessfulAttachVolume` (and no pull is running) | `Starting` | `AttachingVolume` |
| container waiting, otherwise | `Starting` | `CreatingContainer` |
| `ErrImagePull`/`ImagePullBackOff`/`CrashLoopBackOff`/`CreateContainerConfigError` | `Error` | the waiting reason (+ `message`) |
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

**Collision:** if a Desktop `sanitize(user)` exists with a different `spec.user`, the gateway
refuses (409 page) and never touches it. LDAP IDs are immutable, so the mapping is stable.

## 5. Desktop pod

Rendered from the `desktop` and `storage` config sections. Fixed parts:

- Pod: `hostUsers: false` (`desktop.hostUsers`), `automountServiceAccountToken: false`,
  `enableServiceLinks: false`, `serviceAccountName: desktop.serviceAccountName`,
  `runtimeClassName`, `terminationGracePeriodSeconds` (default 120; must exceed the power hook
  timeout plus the session save).
- Container `desktop`: `runAsUser: 0`, `runAsNonRoot: false`, capabilities add `SYS_ADMIN` and `SYS_NICE`,
  `procMount: Unmasked`, `seccompProfile: RuntimeDefault`, `appArmorProfile: Unconfined`
  (`desktop.securityContext` replaces the whole container securityContext when set).
- Readiness probe: TCP on `desktop.port`.
- Volumes:

| mount | volume |
| --- | --- |
| `storage.mountPath` (default `/persist`) | the PVC; the image owns the layout below it |
| `/run` | emptyDir `Memory`, `desktop.runSizeLimit` (default 256Mi) |
| `/dev/shm` | emptyDir `Memory`, `desktop.shmSize` (default 1Gi) |
| `/run/webtop/sa` | projected `serviceAccountToken` (audience `powerApi.audience`, 3600 s, path `token`) |

Env (plus `desktop.extraEnv`):

| name | value |
| --- | --- |
| `WEBTOP_CONTROLLER_URL` | `powerApi.url` (no trailing slash) |
| `WEBTOP_TOKEN_PATH` | `/run/webtop/sa/token` |
| `WEBTOP_DESKTOP` | desktop name (informational) |
| `WEBTOP_USER` | original user ID (informational) |
| `WEBTOP_NAMESPACE` | namespace (informational) |
| `WEBTOP_HTTP_PORT` | `desktop.port` — the web client (Selkies) must listen here, plain HTTP |
| `WEBTOP_PERSIST_PATH` | `storage.mountPath` |

The image's `webtop-init` writes every `WEBTOP_*` variable to `/run/webtop/env`, since systemd units
do not inherit the container env. The controller and agent do not read the three informational ones;
they are there for desktop-side units and scripts.

Optional agent overrides, never set by the controller (the defaults are what ships):

| Variable | Default |
| --- | --- |
| `WEBTOP_POWER_HOOK_TIMEOUT` | `60s`: how long the power hook retries the request |
| `WEBTOP_IDLE_REASON_THRESHOLD` | `60s`: input idleness at poweroff that counts as `idle` |
| `WEBTOP_COMPOSITOR_TIMEOUT` | `3m`: how long KWin may stay non-compositing, once the target and the web port are up, before the agent reports the stuck stage `Compositor not active` |
| `WEBTOP_AGENT_STATE_DIR` | `/run/webtop` |
| `WEBTOP_AGENT_WAYLAND_SOCKET` | unset: the agent finds it from the user manager's `WAYLAND_DISPLAY` |
| `WEBTOP_AGENT_USER` | `user` |

**Image obligations:**

- Serve the web client on `WEBTOP_HTTP_PORT`, and work under a path prefix: load assets and open the
  websocket relative to the page URL. The gateway serves admins at `/u/<name>/` (section 7.3).
- A poweroff from inside must end PID 1 with an exit code in `controller.cleanExitCodes`. The default `[0]` is not yet confirmed against a real
  container exit after a poweroff; set the list to the code the image actually exits with.
- Re-read the token file on every request (kubelet rotates it).

## 6. LDAP

Enabled when `ldap.url` is set; otherwise every user is authorized (`LDAPDisabled`).

- Membership modes (`ldap.membership`):
  - `memberOf` (default): members = entries under `ldap.baseDN` matching
    `(&<userFilter>(<memberAttribute>=<groupDN>))` (`adminGroupDN`, if set, is OR-ed in); `memberAttribute` defaults to `memberOf`.
  - `member`: read `ldap.groupDN`'s (and `adminGroupDN`'s, if set) `memberAttribute` (default `member`) DNs; each DN's
    `userAttribute` is the user ID.
- `userAttribute` (default `uid`) is the user ID compared with `spec.user` (case-sensitive).
- Sync every `ldap.syncInterval` (default 5m), leader only: users not in the set get
  `Authorized=False`; if `On`, they are stopped with reason `ldap`. The Desktop and PVC stay; being
  re-added does not auto-start.
- **Errors never remove anyone.** A failed sync, or a sync returning **zero** members, changes
  nothing and is counted as an error. While LDAP fails, new starts follow `ldap.errorPolicy`:
  `deny` (default) or `allow`.

## 7. Gateway

### 7.1 Identity

- User: header `gateway.userHeader` (default `Remote-User`). Missing → 401.
- Groups: header `gateway.groupsHeader` (default `Remote-Groups`), split on
  `gateway.groupsSeparator` (default `,`), trimmed.
- `gateway.requiredGroup` (optional): users not in it get 403 and nothing is created. Members of `adminGroup` pass it.
- `gateway.adminGroup` (optional): may open any desktop.
- `gateway.trustedProxyCIDRs` (optional): if set, requests whose remote address is outside get 403.
  The gateway does not use `X-Forwarded-For` to decide this.

### 7.2 Paths

| path | handled |
| --- | --- |
| `/u/<user>/…` | desktop `sanitize(<user>)`; allowed for that desktop's own user and admins. Unknown → 404; admins never create desktops |
| everything else | the caller's own desktop |
| `<base>/.webtop/events` | SSE, never proxied |
| `<base>/.webtop/reload.js` | the script injected into the desktop's page (section 7.3), never proxied |
| `<base>/.webtop/start` | POST start, never proxied |
| `/.webtop/healthz` | not used; probes are on `gateway.healthAddr` (`/healthz`, `/readyz`) |

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
| `Authorized=False`, not `Running` | 403 page "no access" (a user re-added to the group is let in after the next LDAP sync) | 503 |
| `Off`, no `status.lastStop` | patch `powerState: On` → boot page | 503 |
| `Off`, `status.lastStop` set | **off page** "desktop is off — start it?" | 503 |
| `On`, not `Running` (incl. `Error`) | boot page | 503 |

503 responses carry `Retry-After: 5` and never start anything. Boot page, off page, SSE and all
gateway error pages send `Cache-Control: no-store`.

**Boot page:** HTML that opens `EventSource("<base>/.webtop/events")` and shows `stage`,
`bootProgress` and `message`; on the `ready` event it reloads the page.

**SSE** `GET <base>/.webtop/events` (same authz as the desktop, never starts it):

```
event: status
data: {"phase":"Starting","stage":"Booting","bootProgress":40,"message":"","authorized":true}

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
"Waiting for volume".

**Off page / start:** a form `POST <base>/.webtop/start` with field `csrf`. CSRF is a double-submit
cookie: the page sets cookie `webtop_csrf` (random 32 bytes, base64url, `HttpOnly`, `SameSite=Strict`,
`Path=/`, `Secure` unless `gateway.cookieSecure=false`) and the same value in the form. The POST is
accepted only if field = cookie (constant-time) and the request is same-origin (`Sec-Fetch-Site:
same-origin` if present, else `Origin` host = `Host`; neither → reject). Success: patch
`powerState: On`, `303` to `<base>/`. Failure: 403.

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

`reason` may be omitted for `reboot`. `ldap`, `exit`, `admin` → 400. Effects, **completed before the response**:

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
 "connectedClients": 1, "idleSeconds": 12}
```

Reports from a pod whose UID ≠ `status.podUID` → `409`, ignored; right after a pod starts the
controller may not have recorded it yet, so the agent retries a 409 (every few seconds). `ready: true` sets `agentReady`.
`stage`/`bootProgress` are only applied before ready. `stage` is free text for the boot page, at
most 64 characters (longer → `400`); it never becomes a metric label value (section 11). Response `204`.

**Readiness:** the agent reports `ready: true` only when `multi-user.target` and the user's `plasma-workspace.target` are active, the web port accepts connections, and KWin is compositing (D-Bus `org.kde.KWin` `/Compositor` `org.kde.kwin.Compositing.active` on the user bus; any compositing type counts). While KWin is not compositing the stage is `Starting compositor`; after `WEBTOP_COMPOSITOR_TIMEOUT` it becomes `Compositor not active` (the agent keeps polling and still turns ready if KWin recovers).

**Agent cadence:** during boot on every stage change (≤ 1/s); after ready every 30 s, and
immediately when `connectedClients` changes.

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
  maxConcurrentReconciles: 4      # Desktops reconciled in parallel; a slow LDAP lookup blocks only one

powerApi:
  addr: ":8082"
  audience: kube-webtop-workspaces
  url: ""                         # required: http://<controller-svc>.<ns>.svc:8082

ldap:
  url: ""                         # ldap:// or ldaps://; empty disables LDAP
  startTLS: false
  caFile: ""
  insecureSkipVerify: false
  bindDN: ""
  bindPasswordFile: ""            # mounted secret
  baseDN: ""
  userFilter: "(objectClass=person)"
  userAttribute: uid
  groupDN: ""
  adminGroupDN: ""               # optional: its members are authorized too
  membership: memberOf            # memberOf | member
  memberAttribute: ""             # default memberOf / member by mode
  syncInterval: 5m
  timeout: 10s
  errorPolicy: deny               # deny | allow

desktop:
  image: ""                       # required
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
  extraVolumes: []                # []corev1.Volume
  extraVolumeMounts: []           # []corev1.VolumeMount
  securityContext: null           # replaces the default container securityContext
  shmSize: 1Gi
  runSizeLimit: 256Mi
  terminationGracePeriodSeconds: 120

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
```

Ports: gateway HTTP `:8080`, controller power API `:8082`, metrics `:9090`, probes `:8081`
(`/healthz`, `/readyz`). Leader election uses a `coordination.k8s.io` Lease.

## 10. RBAC

Generated by controller-gen into `config/rbac/controller/role.yaml` and
`config/rbac/gateway/role.yaml` (as ClusterRoles, the only kind it emits). The chart renders the
namespaced rules as a Role + RoleBinding in the release namespace and only `tokenreviews` as a
ClusterRole + ClusterRoleBinding.

- **gateway** (Role): `desktops` get/list/watch/create/patch. Nothing else.
- **controller** (Role): `desktops` get/list/watch/update/patch; `desktops/status` get/update/patch;
  `deployments`, `services` get/list/watch/create/update/patch; `persistentvolumeclaims`
  get/list/watch/create/patch (**no delete**); `pods` get/list/watch; `events` create/patch/list/watch;
  `leases` get/list/watch/create/update/patch.
  (ClusterRole): `tokenreviews` create.
- **desktop** ServiceAccount: no RBAC at all; its token is used only for the power API audience.

Neither role reads Secrets or ConfigMaps: the config file and `ldap.bindPasswordFile` (and
`ldap.caFile`) are mounted files.

Network (besides DNS):

| component | egress | ingress |
| --- | --- | --- |
| gateway | API server; desktop Services on `desktop.port` | ingress controller on `gateway.addr` |
| controller | API server; `ldap.url` when LDAP is enabled | desktop pods on `powerApi.addr` |
| desktop | controller on `powerApi.addr` (plus whatever the chart allows) | gateway on `desktop.port` |

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
| `webtop_desktop_authorized` | gauge | `desktop` |
| `webtop_desktop_update_pending` | gauge | `desktop` |
| `webtop_desktop_boot_progress` | gauge | `desktop` |
| `webtop_desktop_connected_clients` | gauge | `desktop` |
| `webtop_desktop_idle_seconds` | gauge | `desktop` |
| `webtop_desktop_last_stop_timestamp_seconds` | gauge | `desktop`, `reason` |
| `webtop_desktop_stops_total` | counter | `reason` |
| `webtop_desktop_start_duration_seconds` | histogram | — (powerState On → Running) |
| `webtop_power_requests_total` | counter | `action`, `reason`, `code` |
| `webtop_report_requests_total` | counter | `code` |
| `webtop_ldap_sync_total` | counter | `result` (`success`, `error`) |
| `webtop_ldap_last_success_timestamp_seconds` | gauge | — |
| `webtop_ldap_members` | gauge | — |

Gateway:

| metric | type | labels |
| --- | --- | --- |
| `webtop_gateway_requests_total` | counter | `outcome` (`proxy`, `boot`, `off`, `start`, `unavailable`, `denied`, `notfound`, `error`) |
| `webtop_gateway_websockets` | gauge | — |
| `webtop_gateway_starts_total` | counter | `trigger` (`create`, `navigate`, `button`) |

Plus the controller-runtime and Go/process defaults.
