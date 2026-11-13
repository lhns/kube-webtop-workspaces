# 0023 — /dev/net/tun reaches the desktop as a device plugin resource

- Status: Accepted
- Date: 2026-11-13

## Context

Rootless Podman in the desktop needs `/dev/net/tun` for its own networks (webtop-nix ADR 0017).
Desktop pods are user-namespaced, and the runtime idmaps every volume mount of such a pod. A hostPath
of the device sits on devtmpfs, which the kernel refuses to idmap (`MOUNT_ATTR_IDMAP: invalid argument`),
so the container does not start. Devices the runtime adds itself (the OCI `linux.devices` list) are not
idmapped; in Kubernetes only a device plugin's allocation puts a device there without privilege.

## Decision

- `desktop.tunDevice` makes the desktop container request `limits: {<desktop.tunResource>: 1}`
  (default `webtop.lhns.github.io/tun`) instead of mounting a hostPath.
- The chart serves that resource with a DaemonSet of squat/generic-device-plugin
  (`desktop.tunDevicePlugin`, on with `tunDevice`), image pinned by digest. Each node offers `count`
  slots of the one shared device. A cluster with its own plugin disables it and sets `tunResource`.
- The resource name has this project's domain, so it cannot collide with a generic-device-plugin a
  cluster already runs.

## Consequences

- Without a plugin serving the resource on a node, desktops stay `Pending` there with
  `Insufficient <tunResource>` instead of failing to start.
- The plugin mounts the host's `/dev/net` and the kubelet's device plugin directory; its namespace needs
  Pod Security `privileged`, which the desktops' namespace already has.
- Turning `tunDevice` on or off changes the pod template, so running desktops get `updatePending`.
