# Manual desktop test

Runs the real desktop image (`image/`) as one pod plus a PVC, with no controller or gateway. It
checks the image and the node runtime on their own.

The pod bypasses the controller: the in-pod agent's power calls (suspend, poweroff) go to a
controller URL that does not exist and time out. Everything else works.

## Prerequisites

The node setup in [docs/cluster-requirements.md](../../docs/cluster-requirements.md): user
namespaces, a writable-cgroup RuntimeClass (`pod.yaml` uses `cgroup-writable`; edit it to match),
and a StorageClass. Pick the image in `kustomization.yaml` (`images`); pin a digest for a
reproducible test. The `latest` tag exists only after a release; until then use `main`, `sha-<sha7>` or `0.0.0-dev.<sha7>`. Add a `nodeSelector` to `pod.yaml` to test a specific node.

## Run

```sh
kubectl apply -k test/manual
kubectl -n webtop-test wait pod/desktop-test --for=condition=Ready --timeout=300s
kubectl -n webtop-test logs -f desktop-test
kubectl -n webtop-test exec -i desktop-test -- sh -s < test/manual/check.sh
kubectl -n webtop-test port-forward pod/desktop-test 8080:8080   # then open http://localhost:8080
```

## What to check

`check.sh` covers the first three; look at the desktop in the browser for the rest.

- The pod runs in a user namespace (non-identity `uid_map`), with writable cgroup v2 and an
  unconfined AppArmor profile.
- systemd reaches `running` (or `degraded`), and `/persist` and the Nix overlay store are mounted.
- A Wayland client (konsole) and an X11 client (konsole on xcb) both get a window.
- The desktop renders and takes input in the browser.
- Persistence: write a file under `/home/user` and install a package with `nix`, delete the pod,
  `kubectl apply -k test/manual` again, and both are still there.
- Poweroff: `kubectl -n webtop-test exec desktop-test -- systemctl poweroff` exits the container
  with 0 and the pod goes `Completed`. Pod deletion stops units in order well inside the 120 s
  grace period.

## Clean up

```sh
kubectl -n webtop-test delete pod desktop-test
```

Stop there. Do not delete the PVC or its PV, and not the namespace (that deletes the PVC), until
the data is not wanted. On storage backends that reclaim a volume when its PV is deleted, deleting
the PV destroys the data even with `reclaimPolicy: Retain`.
