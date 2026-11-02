# Spike: suspending a desktop to disk (checkpoint/restore)

Question: can a sleeping desktop be checkpointed to disk and restored later, so that it frees its
RAM and its node, with CRIU or an alternative? Investigated from source and docs, plus read-only
measurements of one live desktop. Nothing was run or changed on the cluster.

Versions read: Kubernetes **v1.36.4** (k0s `v1.36.4+k0s.1`), containerd **v2.3.5** and runc
**v1.4.3** (what that k0s ships,
[Makefile.variables:10,20](https://github.com/k0sproject/k0s/blob/v1.36.4%2Bk0s.1/embedded-bins/Makefile.variables#L10-L20)),
CRIU **v4.3** (Debian 13 has 4.1.1), kernel **6.12** (the workers run `6.12.105+deb13`), systemd
**v262** (the image's, ADR 0009), KEPs at
[`a904b502`](https://github.com/kubernetes/enhancements/tree/a904b50283fe82d0d4f46988d0fd9d1a35a86c25).

Options:

- **A**: checkpoint the whole desktop container with Kubernetes (kubelet checkpoint API, containerd,
  runc, CRIU) and restore it into a new pod from a checkpoint image.
- **B**: the agent runs CRIU inside the pod on the session subtree only, keeping systemd and the pod.
- **C**: today's Sleep (frozen session) plus `memory.reclaim`, which pushes the frozen cgroups'
  memory to swap. RAM is freed and the pod stays.
- **D** (baseline, exists): save the session and power off (ADR 0005/0009,
  `WEBTOP_SUSPEND_MODE=poweroff`).

## Verdict

What each option releases decides it. Only stopping the pod gives back the memory **request**
(1Gi, what the scheduler reserves) and lets a node drain. Options B and C reduce only **actual**
usage. That still matters here: the measured desktop uses 1.8 GiB against its 1Gi request, and
the workers' memory limits are overcommitted (139–168 %).

- **A: no-go.** Restore through containerd is off by default in 2.3.5 and removed in 2.4. Kubernetes
  has no restore API before KEP-5823, whose alpha (1.38) neither stops the source pod nor restores
  on another node. Independent of the API, CRIU refuses this container: systemd gives logind, udevd,
  timesyncd and oomd their own UTS namespace, and CRIU does not dump nested UTS/PID/IPC/user
  namespaces; neither do browser sandboxes. `/run` is an emptyDir, so its content (systemd's state, the
  sockets) is not in the checkpoint. Forcing it through means carrying a deprecated containerd path,
  CRIU 4.3 on every node, unit drop-ins, browsers without their sandbox and a controller pipeline
  that builds images. That is several weeks of work for something that breaks on the next containerd
  minor release.
- **B: no-go.** It frees nothing that C does not free, and the session's processes are connected
  to processes outside the dump (KWin to pixelflux's Wayland socket in Selkies, the session bus,
  PipeWire), which CRIU cannot restore. Browsers hit the nested-namespace refusal here as well.
- **C: conditional go, if the goal is RAM rather than nodes.** It is the cheapest option: about 1–2
  days in the agent, no change to the pod model, and waking stays a thaw. It needs node changes,
  though. The kubelet currently runs `NoSwap`, so the pod cannot swap at all. With `LimitedSwap` and
  today's 5.16 GiB of swap, the 1Gi request yields **≈ 83 MiB** of swap. To be useful, swap must be
  on the order of node RAM, and swap is then allowed for **every** Burstable pod in the cluster.
  Wake latency is unmeasured.
- **D: keep as the way to free a node.** It exists and releases everything. The cost is a cold boot
  (container start to Ready in 11 s with the image cached, 488 s to pull 3.67 GB without it) and app
  state that the session save does not keep.

**Recommendation:** do not build A or B. Decide whether sleeping desktops should give back RAM
(C, which needs the owner's OK for swap and `LimitedSwap`) or nodes (D, already there). If C, run
the VM experiment below first. Revisit A only once KEP-5823 can stop the source pod, containerd has
`RestorePod`, and CRIU can dump nested namespaces.

## Measured (read-only)

`desktop-lhns` on kube-worker-03: one client connected, no user apps, about 20 minutes after boot
(container cgroup `memory.stat`, 10:52 UTC):

| | bytes | |
| --- | --- | --- |
| `memory.current` | 1.79 GiB | `kubectl top`: 1681Mi |
| anon | 1.45 GiB | CPU memory of Selkies/pixelflux, KWin, Plasma |
| shmem | 94 MiB | memfd buffers and the `/run`, `/dev/shm` tmpfs |
| file | 212 MiB | page cache; CRIU does not dump it |
| kernel (slab etc.) | 131 MiB | |

Just after boot (10:34) it was 1.0 GiB. The pod is Burstable (request 1Gi, limit 4Gi). The workers
have 63.8 GiB RAM and 5.16 GiB swap each, with kubelet `failSwapOn: false` and `memorySwap: {}`
(read from `/configz`).

## 1. Kubernetes and containerd checkpoint/restore (option A)

**Checkpoint works in principle. Restore exists only as a deprecated, default-off containerd path.**

- `ContainerCheckpoint` (KEP-2008) is **beta, on by default** in 1.36 and never went GA
  ([kube_features.go:1330-1333](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/features/kube_features.go#L1330-L1333)).
  The kubelet serves `POST /checkpoint/{ns}/{pod}/{container}` on 10250
  ([server.go:661-667](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/kubelet/server/server.go#L661-L667)),
  which needs RBAC on `nodes/checkpoint`
  ([auth.go:125-126](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/kubelet/server/auth.go#L125-L126)).
  It writes a tar to `/var/lib/kubelet/checkpoints/` on the node
  ([kubelet.go:3418-3426](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/kubelet/kubelet.go#L3418-L3426)).
  The only parameter is `timeout`, with no target directory
  ([docs](https://kubernetes.io/docs/reference/node/kubelet-checkpoint-api/)).
- Kubernetes 1.36 has **no restore API**. KEP-2008: "The restore is a container engine only
  operation". The engine-specific route is a pod whose container image is a checkpoint image.
- containerd implements the dump in CRI since v2.0 (containerd#6965). It requires a running
  container and leaves it running
  ([container_checkpoint_linux.go:786](https://github.com/containerd/containerd/blob/v2.3.5/internal/cri/server/container_checkpoint_linux.go#L786)).
  The archive holds the CRIU images, `rootfs-diff.tar`, the spec and the logs, but **no volume
  content**.
- containerd added restore in v2.1 (containerd#10365): `CreateContainer` with an image whose OCI
  index carries `org.criu.checkpoint.container.name` restores instead of starting
  ([:226-232](https://github.com/containerd/containerd/blob/v2.3.5/internal/cri/server/container_checkpoint_linux.go#L226-L232)).
  Since **v2.3.4 it is disabled by default**
  ([:246-248](https://github.com/containerd/containerd/blob/v2.3.5/internal/cri/server/container_checkpoint_linux.go#L246-L248)).
  Turning it on needs `enable_experimental_restore_via_create = true`, and it is removed in v2.4
  ([RELEASES.md:503-508](https://github.com/containerd/containerd/blob/v2.3.5/RELEASES.md#L503-L508);
  removal in containerd#13871, merged 2026-07-31). Its successor is KEP-5823's `RestorePod`.
  An open report says restore-via-create fails with `image ... not found` even when the image is on
  the node (containerd#14096, not checked further).
- A checkpoint image is built with `checkpointctl build`
  ([checkpointctl-build.adoc](https://github.com/checkpoint-restore/checkpointctl/blob/main/docs/checkpointctl-build.adoc)).
  containerd reads the annotation from an OCI *index*, and whether buildah's output satisfies that
  is **unverified**.
- **KEP-5823** (pod-level checkpoint/restore) is `implementable`, with alpha targeted for v1.38
  ([kep.yaml:11-31](https://github.com/kubernetes/enhancements/blob/a904b50283fe82d0d4f46988d0fd9d1a35a86c25/keps/sig-node/5823-pod-level-checkpoint-restore/kep.yaml#L11-L31)).
  Its non-goals for alpha are cross-node restore, stopping the source pod after the checkpoint, and
  shared memory and volumes
  ([README.md:164-194](https://github.com/kubernetes/enhancements/blob/a904b50283fe82d0d4f46988d0fd9d1a35a86c25/keps/sig-node/5823-pod-level-checkpoint-restore/README.md#L164-L194)).
  So even the future native path does not free RAM or a node at alpha.
- Requirements: CRIU ≥ 3.16 for containerd
  ([:161](https://github.com/containerd/containerd/blob/v2.3.5/internal/cri/server/container_checkpoint_linux.go#L161)).
  The **restore of a user-namespaced container with a non-zero host id**, which is what
  `hostUsers: false` produces, is fixed only in **CRIU 4.3** (CRIU#2903, merged 2026-02-26;
  runc#5121 is still open for the integration test). Debian 13 ships 4.1.1, and 4.3 is in experimental
  ([madison](https://api.ftp-master.debian.org/madison?package=criu&text=on)). k0s does not bundle
  CRIU.

What the controller would have to do: call the checkpoint subresource through the API server's node
proxy and wait. It would then copy the tar off the node, build and push a checkpoint image (needs a
registry), and scale the Deployment to 0. To wake, it would set the Deployment's image to the
checkpoint image on a node that has the 3.67 GB base image, with the same PVC, and fall back to a
cold boot whenever restore fails. This would interfere with ADR 0006 (the image applies on the next
start) and with every template change made in between.

## 2. CRIU against this container

Verdicts: **S** supported, **W** workaround, **B** blocker, **U** unknown. CRIU source at
[v4.3](https://github.com/checkpoint-restore/criu/tree/v4.3).

| feature in the pod | verdict | source and notes |
| --- | --- | --- |
| own user namespace (`hostUsers: false`) | S with CRIU ≥ 4.3 | The rootless case is supported "only if other namespaces are created from the container's user namespace" ([criu#2778](https://github.com/checkpoint-restore/criu/issues/2778)), which runc does. The restore fix is CRIU#2903. runc has `TestUsernsCheckpoint`. |
| idmapped volume mounts | U, probably irrelevant | CRIU has no idmap code (only constants in `mount-v2.h`). Every volume is an external bind mount that runc, not CRIU, recreates at restore ([runc criu_linux.go:448-449](https://github.com/opencontainers/runc/blob/v1.4.3/libcontainer/criu_linux.go#L448-L449)). Inference, untested. |
| systemd's sandboxed services | **B** | `ProtectHostname=yes` in [logind](https://github.com/systemd/systemd/blob/v262/units/systemd-logind.service.in#L46), [udevd](https://github.com/systemd/systemd/blob/v262/units/systemd-udevd.service.in#L51), [timesyncd](https://github.com/systemd/systemd/blob/v262/units/systemd-timesyncd.service.in#L34) and [oomd](https://github.com/systemd/systemd/blob/v262/units/systemd-oomd.service.in#L43) gives each its own UTS namespace. CRIU: "Can't dump nested %s namespace" for every type except mount and net ([namespaces.c:462](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/namespaces.c#L462)), and the maintainers do not plan to implement it ([criu#3195](https://github.com/checkpoint-restore/criu/issues/3195), [criu#1430](https://github.com/checkpoint-restore/criu/issues/1430)). Workaround: drop-ins `ProtectHostname=no`. Whether these namespaces exist in the live pod is unverified (step 1). |
| browsers (Chromium zygote, Firefox content processes) | **B** | They use user and PID (or network) namespaces when they can create them ([Chromium](https://chromium.googlesource.com/chromium/src/+/HEAD/docs/linux/sandboxing.md), [Firefox](https://wiki.mozilla.org/Security/Sandbox/Specifics)). This is the same refusal. The only way around it is disabling the browser sandbox, which is not acceptable. |
| systemd as PID 1 in general | U, poor record | Incus: "only very basic containers (non-systemd containers without a network device) can be migrated reliably" ([move_instances.md](https://github.com/lxc/incus/blob/main/doc/howto/move_instances.md)). `DynamicUser=` services fail to dump ([criu#2576](https://github.com/checkpoint-restore/criu/issues/2576)). |
| writable cgroup v2 subtree | U | CRIU has the v2 freezer ([seize.c:130](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/seize.c#L130)). runc/containerd use `--manage-cgroups-mode` soft. Restoring systemd's delegated subtree is untested. [criu#3029](https://github.com/checkpoint-restore/criu/issues/3029) (open): a checkpoint strips `nsdelegate` from the **host's** cgroup2 mount. cgroup BPF programs (systemd `IPAddressDeny=`, `DeviceAllow=`) are not dumped (CRIU has only BPF *map* code). |
| `/run`, `/dev/shm` (memory emptyDir) | **B** for `/run` | They are external bind mounts and are not dumped ([mount.c:1668](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/mount.c#L1668)). The restored pod gets empty ones, so `/run/systemd`, `/run/webtop`, the D-Bus and Nix daemon sockets are gone. Files that are open must exist with the same size, or restore fails ([files-reg.c:2174-2175](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/files-reg.c#L2174-L2175)). Workaround: no `/run` volume, because `webtop-init` already mounts its own tmpfs then ([webtop-init:41-44](../../image/webtop-init#L41-L44)), and CRIU dumps such tmpfs mounts as tar ([filesystems.c:443](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/filesystems.c#L443)). `/run/user/1000`, mounted by systemd, is already dumped that way. |
| overlay `/nix/store` (userxattr, upper on the PVC) | W/U | CRIU only parses overlay mounts ([filesystems.c:832-836](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/filesystems.c#L832-L836)) and remounts them from mountinfo. The upper directory must be unchanged at restore, which it is if nobody touches the PVC. `userxattr` and nesting are untested. |
| RBD ext4 at `/persist` | W | It is an external bind mount, so the PVC must be attached at the same path at restore. RWO is fine, since the old pod is gone by then. |
| memfd, `wl_shm` buffers, sealed memfds, shared anon | S | [memfd.c:100](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/memfd.c#L100). Mappings of files on an external `/dev/shm` fall under the emptyDir row. |
| Unix sockets (Wayland, D-Bus, PipeWire), fds in flight | S inside the dump | All types and in-flight `SCM_RIGHTS` are supported ([sk-unix.c:155](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/sk-unix.c#L155), [sk-queue.c:57-58](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/sk-queue.c#L57-L58)). A peer outside the dump fails ([sk-unix.c:891-895](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/sk-unix.c#L891-L895)), which matters for B. |
| inotify, fanotify, timerfd, eventfd, signalfd, epoll, pidfd | S | [files.c:535-551](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/files.c#L535-L551). Any other anon inode aborts the dump (`:555`). inotify watches on tmpfs need a path lookup that can fail. |
| io_uring | **B if used** | It is unsupported ([criu#2131](https://github.com/checkpoint-restore/criu/issues/2131), [criu#2673](https://github.com/checkpoint-restore/criu/issues/2673)). Nothing in the image is known to use it, and the image's config has no hits, but this is unverified for Qt/glib/PipeWire at these versions. |
| browser websocket (TCP to the gateway) | W, acceptable | Use `--tcp-close`, since a new pod has a new IP. The client reconnects, and a reconnect is already the wake trigger ([sk-inet.c:200-201](https://github.com/checkpoint-restore/criu/blob/v4.3/criu/sk-inet.c#L200-L201)). |
| Nix sandbox, bubblewrap | S here | The build sandbox is off (ADR 0008). Flatpak/bwrap apps would hit the nested-namespace refusal. |
| GPU | S | llvmpipe, no `/dev/dri`, so nothing to dump. |

Nobody has published CRIU of a modern Wayland desktop. The only precedent is the old
[criu.org/VNC](https://criu.org/VNC) how-to (an X11 server and one app).

## 3. Alternatives that keep the pod (options B and C)

### B: CRIU on the session subtree from the agent

You cannot checkpoint the whole container from inside it: CRIU must not be part of the tree it
dumps, and PID 1 is systemd. Dumping only the Plasma and app units would let the agent kill them
and drop their memory, which is the same result as C with far more risk:

- KWin is a Wayland client of pixelflux, which runs inside Selkies, and Selkies stays up. Plasma
  talks to the session bus and PipeWire, which also stay up. These are connected sockets whose
  peers are outside the dump, and CRIU cannot restore them (sk-unix.c above).
- The browser-namespace blocker still applies.
- CRIU would need `CAP_CHECKPOINT_RESTORE` in the pod's user namespace, and a CRIU in the image.

The pod, its request and the node all stay. **No-go.**

### C: frozen session plus `memory.reclaim`

The agent already freezes `plasma-*.service` and `app.slice` (`agent/internal/freeze`). Writing a
size into `memory.reclaim` of those cgroups reclaims that much
([cgroup-v2.rst:1313-1344](https://github.com/torvalds/linux/blob/v6.12/Documentation/admin-guide/cgroup-v2.rst#L1313-L1344),
with the `swappiness=` key in 6.12). The file is `CFTYPE_NS_DELEGATABLE`
([memcontrol.c:4415-4419](https://github.com/torvalds/linux/blob/v6.12/mm/memcontrol.c#L4415-L4419))
and has no capability check, so the delegated subtree may write it. Reclaim acts on pages, not
tasks, so it should work on a frozen cgroup (unverified). Page cache goes for free. Anonymous memory,
the 1.45 GiB that matters, needs swap:

- **NodeSwap is GA since 1.34**
  ([kep.yaml](https://github.com/kubernetes/enhancements/blob/a904b50283fe82d0d4f46988d0fd9d1a35a86c25/keps/sig-node/2400-node-swap/kep.yaml#L35-L36)).
  The default, `NoSwap`, writes `memory.swap.max = 0` on every container cgroup
  ([kuberuntime_container_linux.go:492-517](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/kubelet/kuberuntime/kuberuntime_container_linux.go#L492-L517)).
  Limits are top-down
  ([cgroup-v2.rst:479](https://github.com/torvalds/linux/blob/v6.12/Documentation/admin-guide/cgroup-v2.rst#L479)),
  so **today the session cannot swap at all**, even though the nodes have swap. zswap needs a swap
  slot too, so it is equally blocked (not traced in code).
- `LimitedSwap` gives Burstable containers whose request is below their limit
  `request / node RAM × swap`, and gives none to critical or Guaranteed pods
  ([:214-256](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/kubelet/kuberuntime/kuberuntime_container_linux.go#L214-L256),
  [:520-522](https://github.com/kubernetes/kubernetes/blob/v1.36.4/pkg/kubelet/kuberuntime/kuberuntime_container_linux.go#L520-L522)).
  On these workers that is 1 / 63.8 × 5.16 GiB ≈ **83 MiB**. To swap out about 1.5 GiB with a 1Gi
  request, swap must be about 1.5 × node RAM (~96 GiB per worker). Raising the request instead
  defeats the purpose.
- Memory emptyDirs are mounted `noswap` on kernel ≥ 6.3
  ([swap-memory-management.md:215-229](https://github.com/kubernetes/website/blob/c32f9de1a9e394ea81fb1786718a4c02b25c27b2/content/en/docs/concepts/cluster-administration/swap-memory-management.md#L215-L229)),
  so `/run` and `/dev/shm` stay resident. memfd buffers can swap.
- `LimitedSwap` is a per-node kubelet setting, so every Burstable pod on the node may then swap. The
  docs recommend encrypted swap on SSD and eviction thresholds tuned so that the node swaps before
  it evicts
  ([same page](https://kubernetes.io/docs/concepts/cluster-administration/swap-memory-management/)).
  This is a cluster-wide behaviour change, not a webtop one.
- Wake latency: thawing is instant, then the first frames fault the working set back in. That is
  probably a few hundred MiB at SSD random-read speed, or much faster from zswap. Expect a
  noticeably sluggish first seconds; this is **unmeasured**.

Node changes for C (owner's OK needed, described only): swap on each worker sized to about node
RAM or more (encrypted, SSD; optionally zswap via `zswap.enabled=1`), and kubelet
`memorySwap.swapBehavior: LimitedSwap` through a k0s worker profile in `k0sctl.yaml`.

## 4. Size, storage and time

| | A (whole container) | B / C | D |
| --- | --- | --- | --- |
| data saved | ≈ anon + memfd ≈ 1.5–1.6 GiB idle; up to the 4Gi limit with a browser | C: same amount, to swap | the Plasma session file (KB) |
| where | node disk `/var/lib/kubelet/checkpoints`, then an image in a registry, so a registry and copy step are needed and the PVC cannot be the target | node swap | the PVC |
| suspend time | seconds to write (estimate) plus image build and push | instant freeze, then reclaim in the background | session save plus stop (~seconds) |
| resume time | seconds of CRIU restore (estimate) if the base image is on the node, otherwise plus a 488 s pull, and the checkpoint image pull | instant thaw, then swap-in stalls (unmeasured) | 11 s container start to Ready (cached image), then the Plasma session restore (unmeasured) |
| frees request / node | yes / yes | no / no (only actual RAM, C) | yes / yes |
| node changes | CRIU 4.3 on workers, containerd `enable_experimental_restore_via_create` (gone in 2.4), RBAC `nodes/checkpoint`, a registry | C: swap sized ~RAM, `LimitedSwap` | none |
| effort | several weeks, and it breaks on containerd 2.4 | C: 1–2 days in the agent | done |

## Step-1 experiment (not run)

Nothing touches the cluster or the user's desktop. Both experiments are NixOS VM tests in CI, like
the earlier spike's.

1. **CRIU refusal list (A).** In a NixOS VM with CRIU 4.3 and podman/runc, run the desktop image as
   in `test/manual/pod.yaml`: user namespace, `cgroup_writable`-equivalent delegation, `/run` once
   as a bind mount and once as the image's own tmpfs, and a loop-device ext4 at `/persist`.
   1. Record `/proc/*/ns/uts` and `ns/pid` per process.
   2. Run `podman container checkpoint --export` and read `dump.log` for the first refusal (nested
      UTS is the expected one).
   3. Add drop-ins with `ProtectHostname=no` and repeat without a browser, then with Firefox and
      Chromium, then try a restore.

   The outcome is either the blocker list confirmed or a measured size and time for a dump and
   restore.
2. **Swap wake latency (C).** In a VM with 8 GiB RAM and 16 GiB swap (also with zswap on), run the
   image in a cgroup with `memory.swap.max` unlimited. Sleep the session through the agent, then
   write `memory.reclaim` with `swappiness=200`. Record `memory.current` and `memory.swap.current`,
   then wake through a new Selkies session and time the first frame and the first responsive input.
   The outcome is the RAM freed per desktop and the wake latency, the two numbers the owner needs to
   decide on C.

Only if C is wanted after that: a throwaway Burstable test pod (not a desktop) on one worker with
`LimitedSwap` and swap, with the owner's OK.
