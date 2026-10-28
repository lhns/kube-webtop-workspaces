# kube-webtop-workspaces

Per-user browser desktops: a gateway (auth-header routing, start on navigation, reverse proxy), a
controller (reconciles `Desktop` objects, LDAP sync, power/report API) and the `Desktop` CRD.
Interface details: [docs/contract.md](../../docs/contract.md). Cluster prerequisites:
[docs/cluster-requirements.md](../../docs/cluster-requirements.md).

```sh
helm install webtop oci://ghcr.io/<owner>/charts/kube-webtop-workspaces -n webtop --create-namespace \
  --set desktop.image=registry.example/desktop:1 \
  --set desktop.runtimeClassName=cgroup-writable \
  --set networkPolicy.ingressController.namespaceSelector.matchLabels."kubernetes\.io/metadata\.name"=ingress-nginx
```

Everything lives in the release namespace. Helm does not upgrade `crds/`; apply them yourself on
upgrades that change the CRD.

## Security notes

- **The gateway trusts `Remote-User` / `Remote-Groups`.** Only the authenticating proxy may reach it.
  `networkPolicy` (on by default) admits only `networkPolicy.ingressController`; additionally set
  **`gateway.trustedProxyCIDRs`** to the proxy's pod/node CIDRs (recommended), so any other peer gets
  403 even if the NetworkPolicy is not enforced by the CNI.
- Desktop egress defaults to `0.0.0.0/0`. Put your pod, service and node ranges in
  `networkPolicy.desktopEgress.exceptCIDRs`.
- The LDAP bind password comes from an existing Secret (`ldap.bindSecret`); there is no literal value.
- The controller can never delete PersistentVolumeClaims (no `delete` verb; checked by
  `charts/tests/check.py`). The only cluster-scoped permission is `tokenreviews/create`.

## Values

The sections `names`, `controller`, `powerApi`, `ldap`, `desktop`, `storage`, `gateway`, `metricsAddr`
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
| `ldap.url` | `""` | empty disables LDAP (everyone authorized) |
| `ldap.baseDN`, `groupDN`, `bindDN`, `userFilter`, `userAttribute`, `membership`, `memberAttribute`, `syncInterval`, `timeout`, `errorPolicy`, `startTLS`, `insecureSkipVerify` | see values.yaml | LDAP sync |
| `ldap.bindSecret.name` / `key` | `""` / `password` | existing Secret with the bind password |
| `ldap.caSecret.name` / `key` | `""` / `ca.crt` | existing Secret with a CA bundle |
| `desktop.image` | `""` (required) | desktop image |
| `desktop.runtimeClassName` | `""` | RuntimeClass with writable cgroups (not created by the chart) |
| `desktop.hostUsers` | `false` | user namespace |
| `desktop.port`, `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `podLabels`, `podAnnotations` | see values.yaml | desktop pod scheduling |
| `desktop.extraEnv`, `extraVolumes`, `extraVolumeMounts`, `securityContext` | | extra pod config; `securityContext` replaces the default container one |
| `desktop.shmSize`, `runSizeLimit`, `terminationGracePeriodSeconds` | `1Gi`, `256Mi`, `120` | |
| `desktop.serviceAccountName` | `""` | empty: `<release>-desktop` |
| `desktopServiceAccount.create` | `true` | shared desktop ServiceAccount (token automount off) |
| `storage.storageClassName`, `size`, `accessModes`, `mountPath`, `labels`, `annotations` | cluster default, `50Gi`, `[ReadWriteOnce]`, `/persist` | per-user PVC |
| `gateway.addr`, `userHeader`, `groupsHeader`, `groupsSeparator`, `adminGroup`, `requiredGroup`, `cookieSecure` | see values.yaml | gateway identity |
| `gateway.trustedProxyCIDRs` | `[]` | **set this**; peers outside get 403 |
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

## Development

`make chart-check` lints, renders (defaults, everything on, policies off) and runs
`charts/tests/check.py`: CRD copy in sync with `config/crd` (`make chart-crd` refreshes it), Role
rules identical to `config/rbac`, no PVC `delete`, only `tokenreviews` cluster-wide, policies keep
desktop pods away from the gateway.
