# 0007 — NixOS image with systemd as PID 1 in a user-namespaced pod

- Status: Accepted (2026-10-29: the feasibility test passed; the image ran Plasma in a live pod with user namespaces, writable cgroups and the overlay store)
- Date: 2026-10-28

## Context

The desktop (webtop-nix) needs systemd and logind. Users get root inside their desktop, which must
not mean root on the node. systemd in a container needs a writable cgroup subtree, which
Kubernetes does not delegate by default.

## Decision

- The image is a **NixOS system** (`boot.isContainer`) built with nix2container, with **systemd as
  PID 1** and `SIGRTMIN+3` as stop signal.
- Desktop pods run with **`hostUsers: false`**, so root in the pod is unprivileged on the node.
- A RuntimeClass whose containerd runtime sets **`cgroup_writable`** gives systemd its cgroups.
  `CAP_SYS_ADMIN` (namespaced) and an unconfined AppArmor profile allow the entrypoint's mounts.
- Fallback if writable cgroups are unavailable: the same image with a minimal init and a small
  login1 implementation instead of systemd.

## Consequences

- Clusters need user namespaces and a containerd runtime with `cgroup_writable`
  (`docs/cluster-requirements.md`); the chart does not create the RuntimeClass.
- Desktops are full NixOS systems; their configuration is the image's, the user's own software
  goes through Nix (ADR 0008).
