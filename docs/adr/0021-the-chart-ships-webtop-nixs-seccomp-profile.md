# 0021 — The chart ships webtop-nix's seccomp profile

- Status: Accepted
- Date: 2026-11-12

## Context

RuntimeDefault refuses `pivot_root`, which bubblewrap and containers in the desktop need. webtop-nix
ships `seccomp/desktop.json`: RuntimeDefault plus `pivot_root`. A `Localhost` profile has to be a file
on every node, and it has to match the image that needs it.

## Decision

- `desktop.seccompProfile` sets the desktop container's profile. The default stays RuntimeDefault,
  because a missing `Localhost` profile stops a pod.
- With `securityProfilesOperator: true`, the chart renders the profile as a cluster-scoped
  `SeccompProfile` of the Security Profiles Operator and points the pod at `operator/<name>.json`.
  The profile is `files/desktop-seccomp.json`, a copy of webtop-nix's file at the revision in
  `flake.lock`; CI fails when they differ, so a webtop-nix bump carries its profile.
- A plain `{type: Localhost, localhostProfile}` stays for clusters that install the file another way.

## Consequences

- Clusters using the option need SPO's CRD before the chart, and SPO configured with their kubelet
  root.
- The profile changes only with a webtop-nix bump. No controller code knows about it.
- A desktop container scheduled before the profile is on its node does not start until it is.
