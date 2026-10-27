# Cluster requirements

The chart does not create cluster-level prerequisites. The desktop pods run systemd as PID 1
inside a user namespace, which needs the following.

## Kubernetes and node runtime

- Kubernetes 1.30+ with **user namespaces** (`pod.spec.hostUsers: false`, the chart default
  `desktop.hostUsers: false`). Needs a Linux kernel with idmapped mounts (>= 6.3 recommended) and a
  runtime that supports it (containerd >= 2.0 / CRI-O).
- A **writable-cgroup runtime** for systemd inside the pod. With containerd, add a runtime handler
  whose runc options set `cgroup_writable = true`:

  ```toml
  # /etc/containerd/config.toml (or a drop-in)
  [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.cgroup-writable]
    runtime_type = "io.containerd.runc.v2"
    [plugins."io.containerd.cri.v1.runtime".containerd.runtimes.cgroup-writable.options]
      cgroup_writable = true
  ```

  The section path depends on the containerd version (1.x uses `io.containerd.grpc.v1.cri`).
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
  container needs `SYS_ADMIN`, `procMount: Unmasked` and runs as UID 0 inside the user namespace,
  so the `restricted` and `baseline` profiles reject it).
- A **StorageClass** for the per-user PVCs (`storage.storageClassName`, or a default class). The
  controller never deletes PVCs; reclaiming them is an operator decision.

## Network

- A CNI that **enforces NetworkPolicy** (the chart enables its policies by default). Without
  enforcement the policies are silently ignored and desktop pods can reach the gateway.
- `networkPolicy.ingressController` must select your ingress controller or Gateway-API data plane.
- The gateway trusts the `Remote-User` / `Remote-Groups` headers, so the proxy in front must
  authenticate users, overwrite those headers and strip client-supplied copies. Set
  `gateway.trustedProxyCIDRs` to the proxy's addresses as a second barrier.
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
