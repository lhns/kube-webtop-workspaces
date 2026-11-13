#!/usr/bin/env bash
# On a real kubelet (k3s): the chart's tun device plugin serves desktop.tunResource, and a
# user-namespaced pod requesting it gets a /dev/net/tun it can open (docs/adr/0023).
# Needs root, helm and the k3s binary on PATH.
set -euo pipefail

ns=tun-test
kc() { k3s kubectl "$@"; }
fail() {
  echo "::error::$*"
  kc -n "$ns" get pods -o wide || true
  kc -n "$ns" describe pods || true
  kc -n "$ns" logs -l app.kubernetes.io/component=tun-device-plugin --tail=50 || true
  exit 1
}

modprobe tun
k3s server --disable=traefik,metrics-server,local-storage,servicelb --write-kubeconfig-mode=644 \
  >/tmp/k3s.log 2>&1 &
until kc get nodes 2>/dev/null | grep -q ' Ready'; do sleep 2; done

kc create namespace "$ns"
helm template tun charts/kube-webtop-workspaces -n "$ns" --set desktop.image=x \
  -s templates/tun-device-plugin.yaml | kc -n "$ns" apply -f -
kc -n "$ns" rollout status ds/tun-kube-webtop-workspaces-tun-device-plugin --timeout=180s || fail "plugin not ready"
res=$(helm show values charts/kube-webtop-workspaces | sed -n 's/^  tunResource: //p')
kc -n "$ns" apply -f - <<YAML
apiVersion: v1
kind: Pod
metadata: {name: probe}
spec:
  hostUsers: false
  restartPolicy: Never
  containers:
    - name: probe
      image: docker.io/library/busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
      command: [sh, -c, 'grep -qv "^ *0 *0 *4294967295" /proc/self/uid_map && test -c /dev/net/tun && : <> /dev/net/tun && echo tun-ok']
      resources:
        limits: {$res: 1}
YAML
kc -n "$ns" wait pod/probe --for=jsonpath='{.status.phase}'=Succeeded --timeout=180s || fail "probe pod did not succeed"
[ "$(kc -n "$ns" logs probe)" = tun-ok ] || fail "probe: $(kc -n "$ns" logs probe)"
echo "ok: /dev/net/tun through $res in a user-namespaced pod"
