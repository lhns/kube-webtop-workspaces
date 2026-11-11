# kube-webtop-workspaces

Per-user browser desktops: a gateway (auth-header routing, start on navigation, reverse proxy), a
controller (reconciles `Desktop` objects, optional unseen sweep, power/report API) and the `Desktop` CRD.
Interface details: [docs/contract.md](../../docs/contract.md). Cluster prerequisites:
[docs/cluster-requirements.md](../../docs/cluster-requirements.md).

```sh
helm install webtop oci://ghcr.io/lhns/charts/kube-webtop-workspaces -n webtop --create-namespace \
  --set desktop.image=registry.example/desktop:1 \
  --set desktop.runtimeClassName=cgroup-writable \
  --set networkPolicy.ingressController.namespaceSelector.matchLabels."kubernetes\.io/metadata\.name"=ingress-nginx
```

Everything lives in the release namespace. Helm does not upgrade `crds/`; apply them yourself on
upgrades that change the CRD.

## Security notes

- **The gateway trusts `Remote-User` / `Remote-Groups`.** Only the authenticating proxy may reach it.
  `networkPolicy` (on by default) admits only `networkPolicy.ingressController`; that is the real gate.
  `gateway.trustedProxyCIDRs` (optional) also makes the gateway refuse other peers and keep their
  `X-Forwarded-*` only from the proxy. It adds little when the proxy shares the pod CIDR with the
  desktops; use it with a CIDR that excludes the desktop pods (e.g. node addresses).
- Desktop egress defaults to `0.0.0.0/0`. Put your pod, service and node ranges in
  `networkPolicy.desktopEgress.exceptCIDRs`.
- A key in `desktop.nix.extraTrustedPublicKeys` is trusted by every desktop's store: whoever can
  push to that cache can put code on every desktop. Give desktops read access only, unless that
  shared trust is what you want.
- There is no directory client: access is the gateway's check of the login proxy's group headers.
- The controller can never delete PersistentVolumeClaims (no `delete` verb; checked by
  `charts/tests/check.py`); it may delete `desktops` only, for the `desktops remove` CLI. The only cluster-scoped permission is `tokenreviews/create`.

## Values

The sections `names`, `controller`, `powerApi`, `desktop`, `storage`, `gateway`, `metricsAddr`
and `healthAddr` map 1:1 onto the config file (contract section 9); `values.schema.json` rejects
unknown keys. Defaults are in `values.yaml`.

| key | default | description |
| --- | --- | --- |
| `image.repository` / `tag` / `pullPolicy` | `ghcr.io/lhns/kube-webtop-workspaces` / appVersion / `IfNotPresent` | controller and gateway image |
| `imagePullSecrets` | `[]` | for the controller/gateway pods |
| `logLevel` / `logFormat` | `info` / `json` | binary flags |
| `names.prefix` | `desktop-` | prefix of per-user Deployment/Service/PVC |
| `controller.leaderElection`, `leaderElectionID`, `requireAgentReady` | `true`, `kube-webtop-workspaces`, `true` | |
| `controller.cleanExitCodes` | `[0]` | exit codes of desktop PID 1 meaning "powered off"; set to what the image exits with |
| `controller.maxConcurrentReconciles` | `4` | desktops reconciled in parallel |
| `powerApi.addr` / `audience` | `:8082` / `kube-webtop-workspaces` | power/report API |
| `powerApi.url` | `""` | URL the desktop agent uses; empty: the controller Service |
| `desktop.image` | `""` (required) | desktop image |
| `desktop.stopAfterUnseen` | `0s` | stop (data kept) a desktop whose owner the gateway has not served for this long; `0s` = never, else at least `24h` (contract section 6) |
| `gateway.displayNameHeader` | `Remote-Name` | header with the user's display name (desktop account name) |
| `gateway.logoutUrl` | `""` | absolute http(s) URL the browser goes to when the desktop session logs out; empty: nowhere |
| `gateway.ssoLogoutDefaults.onLogout` | `true` | sign out at the provider on logout, unless the user set `onLogout` in `~/.config/webtop/session.conf` |
| `gateway.lockLogsOut` | `false` | offer Lock in the desktop; using it signs the browser out at the provider while the session keeps running. Needs `gateway.logoutUrl` (the chart fails without it). `false`: Lock is hidden and blocked |
| `gateway.ssoLogoutDefaults.onLock` | `true` | with `lockLogsOut`, sign out at the provider on Lock, unless the user set `onLock` in `~/.config/webtop/session.conf` (`false`: Lock does nothing for that user) |
| `desktop.runtimeClassName` | `""` | RuntimeClass with writable cgroups (not created by the chart) |
| `desktop.hostUsers` | `false` | user namespace |
| `desktop.port`, `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `podLabels`, `podAnnotations` | see values.yaml | desktop pod scheduling |
| `desktop.extraEnv`, `extraVolumes`, `extraVolumeMounts`, `securityContext` | | extra pod config; `securityContext` replaces the default container one |
| `desktop.nix.extraSubstituters` / `extraTrustedPublicKeys` | `[]` / `[]` | extra binary caches in the desktops' `nix.conf`, after cache.nixos.org (passed as `WEBTOP_NIX_*` env) |
| `desktop.maxAppScreens` | `4` | apps `/apps/<id>` may show at once (1-8), one Selkies screen each; each costs a KWin output plus a capture and an encoder (passed as `WEBTOP_MAX_APP_SCREENS` env) |
| `desktop.defaults.timeZone`, `locale`, `language`, `formats`, `xdgConfig`, `homeModule` | `""`, `""`, `""`, `{}`, `{}`, `""` | defaults of every desktop: IANA time zone, `LANG`, `LANGUAGE`, `LC_*` formats (e.g. `{LC_TIME: sv_SE.UTF-8}` for ISO dates), KDE config files (`xdgConfig.<name>` is ini text; not `plasma-localerc`) and a home-manager module. Users override each. Rendered as a ConfigMap mounted at `/run/webtop/defaults-source`; a change marks running desktops `updatePending` and applies at their next start. Readable by every desktop user: no secrets. Replaces `desktop.timeZone` / `desktop.locale`, which now fail the render (docs/contract.md, "Desktop defaults") |
| `desktop.softwareCenter.unfreeListedDefault` | `true` | whether the Nix Software Center lists unfree packages until the user chooses otherwise in the app (passed as `NSC_UNFREE_LISTED_DEFAULT` env) |
| `desktop.prePull.enabled` / `pauseImage` / `priorityClassName` / `resources` | `false` / pinned `registry.k8s.io/pause` / `""` / tiny | DaemonSet that keeps `desktop.image` pulled on every node desktops can run on, so a start after an image update does not wait for the pull. Its container runs `pauseImage`; the desktop image is attached as a read-only image volume (Kubernetes 1.36 GA), so nothing from it runs and kubelet image GC counts it as in use. Uses the desktop's `nodeSelector`, `tolerations`, `affinity` and pull settings, none of its privileges. Disk: each node keeps the current image (about 2-3 GiB compressed plus 6-8 GiB unpacked) and, during a rollover, the previous one until kubelet image GC removes it. A failed pull of a new pin shows on the pod. Recommended where desktop start latency matters |
| `desktop.selkies` | `{}` | `SELKIES_*` settings (strings, one line, at most 256 characters) for every desktop; override the image defaults, are overridden by the user's allowlisted file. Not a security boundary: the user can edit their own units |
| `desktop.title` | `{{ .User }} – Desktop` | text/template for the browser tab title (`.User`, `.DisplayName`, `.Desktop`; data, never template text); sent as `SELKIES_UI_TITLE` unless `desktop.selkies` sets it; `""` keeps Selkies' title. Applies on the desktop's next start |
| `desktop.shmSize`, `runSizeLimit`, `terminationGracePeriodSeconds` | `1Gi`, `256Mi`, `420` | |
| `desktop.serviceAccountName` | `""` | empty: `<release>-desktop` |
| `desktopServiceAccount.create` | `true` | shared desktop ServiceAccount (token automount off) |
| `storage.storageClassName`, `size`, `accessModes`, `mountPath`, `labels`, `annotations` | cluster default, `50Gi`, `[ReadWriteOnce]`, `/persist` | per-user PVC |
| `gateway.addr`, `userHeader`, `groupsHeader`, `groupsSeparator`, `adminGroup`, `requiredGroup`, `cookieSecure` | see values.yaml | gateway identity |
| `gateway.trustedProxyCIDRs` | `[]` | optional; peers outside get 403 (the NetworkPolicy is the gate) |
| `controllerPod.*`, `gatewayPod.*` | replicas `2`, PDB `minAvailable: 1` | resources, scheduling, labels, extraEnv, PDB; soft node anti-affinity when `affinity` is empty |
| `gatewayService.type` / `port` / `annotations` | `ClusterIP` / `80` | |
| `ingress.*` | disabled | `className`, `annotations`, `hosts`, `path`, `pathType`, `tls` |
| `httpRoute.*` | disabled | `parentRefs`, `hostnames`, `path`, `annotations` |
| `metrics.prometheusAnnotations` | `false` | `prometheus.io/*` pod annotations |
| `metrics.serviceMonitor.enabled` | `false` | also `interval`, `scrapeTimeout`, `labels` |
| `networkPolicy.enabled` | `true` | all policies below |
| `networkPolicy.ingressController` | namespace `ingress-nginx` | `namespaceSelector` / `podSelector` of the only peers allowed to reach the gateway; at least one required |
| `networkPolicy.dns` | `kube-system` / `k8s-app=kube-dns` | desktop egress to DNS |
| `networkPolicy.metricsFrom` | `[]` | peers allowed to scrape metrics (nobody by default) |
| `networkPolicy.desktopEgress.allowCIDRs` / `exceptCIDRs` | `["0.0.0.0/0"]` / `[]` | desktop internet egress; list cluster ranges in `exceptCIDRs` |
| `networkPolicy.desktopEgress.extraRules` | `[]` | egress rules appended as-is, e.g. to an in-cluster cache that `exceptCIDRs` hides |

## Development

`make chart-check` lints, renders (defaults, everything on, policies off) and runs
`charts/tests/check.py`: CRD copy in sync with `config/crd` (`make chart-crd` refreshes it), Role
rules identical to `config/rbac`, no PVC `delete`, only `tokenreviews` cluster-wide, policies keep
desktop pods away from the gateway.

## Removing a user

Remove them at the login provider: they are refused from their next request. Their running desktop
stays up until stopped. In the controller pod:

```sh
kubectl -n <ns> exec deploy/<release>-controller -- /kube-webtop-workspaces desktops list
kubectl -n <ns> exec deploy/<release>-controller -- /kube-webtop-workspaces desktops stop <user>
kubectl -n <ns> exec deploy/<release>-controller -- /kube-webtop-workspaces desktops remove <user>
```

`remove` deletes the Desktop and keeps the PVC (its name is printed); deleting the PVC and volume is
a manual step. `desktop.stopAfterUnseen` does the stopping by itself.

## Logout

With `gateway.logoutUrl` set, logging out of the desktop session also signs the browser out at the
login provider: the value is any absolute http(s) URL, such as the provider's logout endpoint with a return address; kww does not interpret it. Empty: nothing
is redirected and the session comes back. The URL is read from the gateway config only, never put in
a page.

### Lock

Lock never locks or ends the session. With `gateway.lockLogsOut: true` (and a `logoutUrl`) Plasma offers
Lock, and using it signs the browser out at the provider; signing back in lands in the same session,
with its apps still running. The desktop clears the lock at once, so there is no lock screen (its
greeter cannot be used here). A user whose `onLock` is `false` (home-manager `webtop.ssoLogout.onLock`,
else `gateway.ssoLogoutDefaults.onLock`) still sees Lock, but it does nothing for them. With
`lockLogsOut: false`, the default, Lock is hidden and blocked as before. A change of `lockLogsOut`
applies when the desktop session starts (a pod restart or a Log Out).
