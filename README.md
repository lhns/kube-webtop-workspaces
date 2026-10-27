# kube-webtop-workspaces

Persistent per-user browser desktops on Kubernetes. Every authorized user gets their own desktop pod and
volume. The pod is created on first use, shows a boot screen while it starts, and stops only when the
user shuts it down (or idles out, per their own desktop settings).

## Components

One Go binary (`cmd/kube-webtop-workspaces`), run as two Deployments:

- **gateway** (`--mode=gateway`): sits behind a forward-auth proxy that sets the user and groups
  headers. It reverse-proxies each user to their own desktop (HTTP and websockets), starts it on a
  top-level navigation, and serves the boot screen and the "desktop is off" page. Admins can open any
  desktop at `/u/<user>/`.
- **controller** (`--mode=controller`): reconciles `Desktop` resources
  (`webtop.lhns.github.io/v1alpha1`) into a PVC, Service and Deployment, and checks LDAP group
  membership. It also serves the power/report API that the in-pod agent uses to report shutdowns,
  reboots, boot progress and session activity. PVCs are never deleted by the controller.

The desktop image and the Helm chart are separate parts of the project.

**Interface:** [docs/contract.md](docs/contract.md) covers the CRD, labels, the power/report API,
pod environment, gateway behaviour, configuration, RBAC and metrics.

## Development

```sh
make generate   # deepcopy, CRD (config/crd), RBAC (config/rbac)
make build vet
make test       # unit + envtest; setup-envtest downloads kube-apiserver/etcd
```

envtest has no Windows binaries. On Windows, run the tests in a Linux container:

```sh
tar -cf - --exclude=.git . | docker run -i --rm -v kww-gocache:/root/.cache -v kww-gomod:/go/pkg/mod golang:1.26 \
  sh -c 'mkdir /src && tar -xf - -C /src && cd /src && make test'
```

## License

Apache-2.0
