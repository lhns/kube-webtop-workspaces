# Cluster requirements

The chart does not create cluster-level prerequisites. The desktop pods run systemd as PID 1
inside a user namespace, which needs the following.

## Kubernetes and node runtime

- Kubernetes 1.30+ with **user namespaces** (`pod.spec.hostUsers: false`, the chart default
  `desktop.hostUsers: false`). Needs a Linux kernel with idmapped mounts (>= 6.3 recommended) and a
  runtime that supports it (containerd >= 2.0 / CRI-O).
- A **writable-cgroup runtime** for systemd inside the pod: containerd >= 2.1, a runtime handler
  with `cgroup_writable = true` (a key of the runtime itself, not of its runc `options`):

  ```toml
  # /etc/containerd/config.toml (or a drop-in)
  [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.cgroup-writable]
    runtime_type = "io.containerd.runc.v2"
    cgroup_writable = true
    [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.cgroup-writable.options]
      SystemdCgroup = true  # match the node's cgroup driver
  ```
- A **RuntimeClass** naming that handler, then `desktop.runtimeClassName` set to it:

  ```yaml
  apiVersion: node.k8s.io/v1
  kind: RuntimeClass
  metadata:
    name: cgroup-writable
  handler: cgroup-writable
  ```

- **AppArmor**: desktop pods run with `appArmorProfile: Unconfined` (set by the controller; see
  contract section 5). Nodes that enforce a default AppArmor profile need to permit this; clusters
  using Pod Security Admission must exempt or label the namespace accordingly (the desktop
  container needs `SYS_ADMIN`, `SYS_NICE` (to exec NixOS's kwin_wayland capability wrapper; real-time
  priority still fails harmlessly in a user namespace), `procMount: Unmasked` and runs as UID 0 inside the user namespace,
  so the `restricted` and `baseline` profiles reject it).
- With `desktop.seccompProfile.securityProfilesOperator`: the
  [Security Profiles Operator](https://github.com/kubernetes-sigs/security-profiles-operator) (v1 API)
  installed first, with its `kubeletDir` set to the nodes' kubelet root (k0s: `/var/lib/k0s/kubelet`).
  It installs the chart's `SeccompProfile` on every node, also on nodes added later. A desktop
  container scheduled before the file is on its node does not start; the kubelet retries until it is.
- With `desktop.seccompProfile` set to a `Localhost` profile: the file on every node desktops can run
  on, under the kubelet's seccomp directory (`<kubelet root-dir>/seccomp/`: `/var/lib/kubelet/seccomp/`,
  k0s `/var/lib/k0s/kubelet/seccomp/`), before the value is set; a pod whose profile is missing does
  not start. For bubblewrap and containers in the desktop, use webtop-nix's `seccomp/desktop.json`
  (containerd's RuntimeDefault plus `pivot_root`) at a pinned commit:

  ```sh
  curl -fsSLo /var/lib/kubelet/seccomp/webtop-desktop.json \
    https://raw.githubusercontent.com/lhns/webtop-nix/<commit>/seccomp/desktop.json
  ```

  then `desktop.seccompProfile: {type: Localhost, localhostProfile: webtop-desktop.json}`.
- With `desktop.tunDevice`: `/dev/net/tun` on the nodes (the `tun` module). The default device rules of
  containerd/runc allow it, so the pod stays unprivileged.
- A **StorageClass** for the per-user PVCs (`storage.storageClassName`, or a default class). The
  controller never deletes PVCs; reclaiming them is an operator decision.

## Network

- A CNI that **enforces NetworkPolicy** (the chart enables its policies by default). Without
  enforcement the policies are silently ignored and desktop pods can reach the gateway.
- `networkPolicy.ingressController` must select your ingress controller or Gateway-API data plane.
- The gateway trusts the `Remote-User` / `Remote-Groups` headers, so the proxy in front must
  authenticate users, overwrite those headers and strip client-supplied copies. The
  NetworkPolicy is the gate; `gateway.trustedProxyCIDRs` adds nothing when the proxy shares the pod
  CIDR with the desktops.
- Desktop egress is `0.0.0.0/0` by default. List the pod, service and node CIDRs in
  `networkPolicy.desktopEgress.exceptCIDRs` so desktops cannot reach cluster internals (the API
  server, other nodes, cluster services). DNS and the controller power API stay allowed.
- Node-originated traffic (kubelet probes) is normally exempt from NetworkPolicy; some CNIs need
  an explicit allowance.

## Installing

Helm installs `crds/` once and never upgrades them. On a chart upgrade that changes the CRD, apply it
first:

```sh
kubectl apply -f charts/kube-webtop-workspaces/crds/
```

## Trying the node setup

`test/manual/` runs the desktop image as a single pod without the controller, to check these
requirements on a node: see [its README](../test/manual/README.md).
