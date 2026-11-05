#!/usr/bin/env python3
"""Chart checks: CRD copy in sync, RBAC mirrors config/rbac, never `delete` on PVCs, secrets handling.

Needs helm and PyYAML. Run from anywhere: python3 charts/tests/check.py
"""
import filecmp
import pathlib
import subprocess
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]
CHART = ROOT / "charts" / "kube-webtop-workspaces"
TESTS = ROOT / "charts" / "tests"
KUBE_VERSION = "1.31.0"
errors = []

# Desktop pod labels are interface (contract 3.1); the policies must select exactly these.
DESKTOP_LABELS = {"app.kubernetes.io/name": "webtop-desktop", "app.kubernetes.io/managed-by": "kube-webtop-workspaces"}


def fail(msg):
    errors.append(msg)
    print("FAIL:", msg)


def render(*args):
    out = subprocess.run(
        ["helm", "template", "r", str(CHART), "-n", "ns", "--kube-version", KUBE_VERSION, *args],
        check=True, capture_output=True, text=True,
    ).stdout
    return [d for d in yaml.safe_load_all(out) if d]


def rules_of(doc):
    """Normalized (apiGroup, resource, verbs) triples of a Role/ClusterRole."""
    return {
        (g, r, tuple(sorted(rule["verbs"])))
        for rule in doc.get("rules", [])
        for g in rule.get("apiGroups", [""])
        for r in rule["resources"]
    }


# 1. CRD copy.
src = ROOT / "config/crd/webtop.lhns.github.io_desktops.yaml"
dst = CHART / "crds/webtop.lhns.github.io_desktops.yaml"
if not dst.exists() or not filecmp.cmp(src, dst, shallow=False):
    fail(f"{dst.relative_to(ROOT)} differs from {src.relative_to(ROOT)}; run `make chart-crd`")

# 2. Expected RBAC from controller-gen output.
expected = {}
for comp in ("controller", "gateway"):
    doc = yaml.safe_load((ROOT / f"config/rbac/{comp}/role.yaml").read_text())
    expected[comp] = rules_of(doc)
cluster_rule = ("authentication.k8s.io", "tokenreviews", ("create",))
if cluster_rule not in expected["controller"]:
    fail("config/rbac/controller no longer has tokenreviews; update the chart")
expected["controller"].discard(cluster_rule)

cases = {
    "defaults": ["--set", "desktop.image=x"],
    "all-on": ["-f", str(TESTS / "values-all-on.yaml")],
    "netpol-off": ["-f", str(TESTS / "values-netpol-off.yaml")],
}
for name, args in cases.items():
    docs = render(*args)
    kinds = [d["kind"] for d in docs]

    for d in docs:
        if d["kind"] not in ("Role", "ClusterRole"):
            continue
        for rule in d["rules"]:
            res, verbs = rule["resources"], rule["verbs"]
            if "*" in verbs or "*" in res:
                fail(f"{name}: wildcard in {d['kind']} {d['metadata']['name']}")
            if "persistentvolumeclaims" in res and {"delete", "deletecollection", "*"} & set(verbs):
                fail(f"{name}: {d['metadata']['name']} may delete persistentvolumeclaims")

    roles = {d["metadata"]["name"].rsplit("-", 1)[1]: d for d in docs if d["kind"] == "Role"}
    for comp in ("controller", "gateway"):
        if rules_of(roles[comp]) != expected[comp]:
            fail(f"{name}: Role {comp} differs from config/rbac/{comp}/role.yaml: "
                 f"{rules_of(roles[comp]) ^ expected[comp]}")
    cluster = [d for d in docs if d["kind"] == "ClusterRole"]
    if len(cluster) != 1 or rules_of(cluster[0]) != {cluster_rule}:
        fail(f"{name}: ClusterRole must contain only tokenreviews/create")

    cm = next(d for d in docs if d["kind"] == "ConfigMap")
    cfg = yaml.safe_load(cm["data"]["config.yaml"])
    if "bindPassword" in cfg["ldap"] or "bindSecret" in cfg["ldap"] or "caSecret" in cfg["ldap"]:
        fail(f"{name}: chart-only ldap keys leaked into the config")
    if name == "all-on" and cfg["ldap"].get("bindPasswordFile") != "/etc/kube-webtop-workspaces-secrets/ldap/password":
        fail(f"{name}: bindPasswordFile not set from bindSecret")
    if {"nix", "softwareCenter", "maxAppScreens"} & cfg["desktop"].keys():
        fail(f"{name}: chart-only desktop.nix, softwareCenter or maxAppScreens leaked into the config")
    nsc = [e["value"] for e in cfg["desktop"].get("extraEnv") or [] if e["name"] == "NSC_UNFREE_LISTED_DEFAULT"]
    want_nsc = ["0"] if name == "all-on" else ["1"]
    if nsc != want_nsc:
        fail(f"{name}: NSC_UNFREE_LISTED_DEFAULT {nsc}, want {want_nsc}")
    cap = [e["value"] for e in cfg["desktop"].get("extraEnv") or [] if e["name"] == "WEBTOP_MAX_APP_SCREENS"]
    want_cap = ["2"] if name == "all-on" else ["4"]
    if cap != want_cap:
        fail(f"{name}: WEBTOP_MAX_APP_SCREENS {cap}, want {want_cap}")
    nix_env = {e["name"]: e["value"] for e in cfg["desktop"].get("extraEnv") or [] if e["name"].startswith("WEBTOP_NIX_")}
    want_env = {"WEBTOP_NIX_EXTRA_SUBSTITUTERS": "http://cache.example.org/a http://cache.example.org/b",
                "WEBTOP_NIX_EXTRA_TRUSTED_PUBLIC_KEYS": "a:AAAA b:BBBB"} if name == "all-on" else {}
    if nix_env != want_env:
        fail(f"{name}: desktop WEBTOP_NIX_* env {nix_env}, want {want_env}")
    if name == "all-on" and {"name": "FOO", "value": "bar"} not in cfg["desktop"]["extraEnv"]:
        fail(f"{name}: desktop.nix replaced desktop.extraEnv instead of appending")

    np = {d["metadata"]["name"].rsplit("-", 1)[1]: d for d in docs if d["kind"] == "NetworkPolicy"}
    if name == "netpol-off":
        if np:
            fail(f"{name}: NetworkPolicies rendered while disabled")
    else:
        if set(np) != {"gateway", "controller", "desktop"}:
            fail(f"{name}: expected gateway/controller/desktop NetworkPolicies, got {sorted(np)}")
        else:
            # The gateway must never admit desktop pods (it trusts Remote-User).
            for rule in np["gateway"]["spec"]["ingress"]:
                for peer in rule["from"]:
                    sel = peer.get("podSelector", {}).get("matchLabels", {})
                    if sel.get("app.kubernetes.io/name") == "webtop-desktop":
                        fail(f"{name}: gateway NetworkPolicy admits desktop pods")
            if np["desktop"]["spec"]["ingress"][0]["from"][0]["podSelector"]["matchLabels"][
                    "app.kubernetes.io/component"] != "gateway":
                fail(f"{name}: desktop ingress is not limited to the gateway")
            desktop_ingress = {p["port"] for r in np["desktop"]["spec"]["ingress"] for p in r["ports"]}
            # 8081 is the agent port of docs/contract.md 7.7 (names.AgentPort, apps.Port).
            if desktop_ingress != {8080, 8081}:
                fail(f"{name}: desktop ingress ports {sorted(desktop_ingress)}, want the web client 8080 and the agent API 8081")
            if len(np["desktop"]["spec"]["ingress"]) != 1:
                fail(f"{name}: the agent API must be reachable only by the gateway; desktop ingress has several rules")
            if np["desktop"]["spec"]["podSelector"]["matchLabels"] != DESKTOP_LABELS:
                fail(f"{name}: desktop policy does not select the controller's desktop labels")
            if np["controller"]["spec"]["ingress"][0]["from"][0]["podSelector"]["matchLabels"] != DESKTOP_LABELS:
                fail(f"{name}: power API ingress is not limited to desktop pods")
            dns_ports = {(p["protocol"], p["port"]) for p in np["desktop"]["spec"]["egress"][0]["ports"]}
            if dns_ports != {("UDP", 53), ("TCP", 53)}:
                fail(f"{name}: DNS egress must allow UDP and TCP 53, got {sorted(dns_ports)}")
            extra = [r for r in np["desktop"]["spec"]["egress"] if any("namespaceSelector" in p and
                     p["namespaceSelector"]["matchLabels"].get("kubernetes.io/metadata.name") == "cache" for p in r["to"])]
            if len(extra) != (1 if name == "all-on" else 0):
                fail(f"{name}: desktopEgress.extraRules not rendered exactly as given")
    if name == "all-on":
        for k in ("Ingress", "HTTPRoute", "ServiceMonitor"):
            if k not in kinds:
                fail(f"{name}: {k} not rendered")
    elif any(k in kinds for k in ("Ingress", "HTTPRoute", "ServiceMonitor")):
        fail(f"{name}: optional resources rendered by default")

    ds = [d for d in docs if d["kind"] == "DaemonSet"]
    if name != "all-on":
        if ds:
            fail(f"{name}: desktop pre-pull DaemonSet rendered by default")
    elif len(ds) != 1:
        fail(f"{name}: want one pre-pull DaemonSet, got {len(ds)}")
    else:
        pod = ds[0]["spec"]["template"]["spec"]
        c = pod["containers"][0]
        vols = {v["name"]: v for v in pod.get("volumes", [])}
        if any(x["image"] == "example.org/desktop:1" for x in pod["containers"]) or "command" in c or "args" in c:
            fail(f"{name}: pre-pull pod must not run the desktop image")
        if not c["image"].startswith("registry.k8s.io/pause:") or "@sha256:" not in c["image"]:
            fail(f"{name}: pre-pull container must run the pinned pause image: {c['image']}")
        if vols.get("desktop-image", {}).get("image", {}).get("reference") != "example.org/desktop:1":
            fail(f"{name}: pre-pull pod must attach the desktop image as an image volume")
        if c.get("volumeMounts") != [{"name": "desktop-image", "mountPath": "/desktop-image", "readOnly": True}]:
            fail(f"{name}: pre-pull image volume must be mounted read-only")
        if c["securityContext"] != {"runAsNonRoot": True, "runAsUser": 65534, "runAsGroup": 65534,
                                    "allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                                    "capabilities": {"drop": ["ALL"]}, "seccompProfile": {"type": "RuntimeDefault"}}:
            fail(f"{name}: pre-pull securityContext is not Pod Security restricted: {c['securityContext']}")
        if pod.get("automountServiceAccountToken") is not False or "serviceAccountName" in pod:
            fail(f"{name}: pre-pull pod must not mount a ServiceAccount token")
        if "runtimeClassName" in pod or "hostUsers" in pod or "securityContext" in pod:
            fail(f"{name}: pre-pull pod must not carry the desktop's runtimeClassName/hostUsers")
        if pod.get("nodeSelector") != {"kubernetes.io/os": "linux"} or pod.get("tolerations") != [{"key": "gpu", "operator": "Exists"}]:
            fail(f"{name}: pre-pull pod does not use the desktop nodeSelector/tolerations")
        if ds[0]["spec"]["updateStrategy"]["rollingUpdate"]["maxUnavailable"] != "100%":
            fail(f"{name}: pre-pull DaemonSet must roll all nodes at once")
        labels = ds[0]["spec"]["template"]["metadata"]["labels"]
        if any(labels.get(k) != v for k, v in ds[0]["spec"]["selector"]["matchLabels"].items()):
            fail(f"{name}: pre-pull selector does not match its pod labels")
        if labels.get("app.kubernetes.io/name") == "webtop-desktop":
            fail(f"{name}: pre-pull pod carries the desktop labels")
    if "prePull" in cfg["desktop"]:
        fail(f"{name}: chart-only desktop.prePull leaked into the config")

    for d in docs:
        if d["kind"] == "Deployment":
            labels = d["spec"]["template"]["metadata"]["labels"]
            sel = d["spec"]["selector"]["matchLabels"]
            if any(labels.get(k) != v for k, v in sel.items()):
                fail(f"{name}: selector of {d['metadata']['name']} does not match its pod labels")
    print(f"ok: {name} ({len(docs)} objects)")

# 3. Invalid values must be refused by the chart's own message, not by some other render error.
def refuse(want, *args):
    bad = subprocess.run(["helm", "template", "r", str(CHART), "--kube-version", KUBE_VERSION, *args],
                         capture_output=True, text=True)
    if bad.returncode == 0:
        fail(f"rendered, want a failure with {want!r}: {args}")
    elif want not in bad.stderr:
        fail(f"failed without {want!r}: {args}\n{bad.stderr}")


refuse("desktop.image is required")
# Null drops the default namespaceSelector; the default podSelector is empty.
refuse("networkPolicy.ingressController needs a namespaceSelector",
       "--set", "desktop.image=x", "--set-json", "networkPolicy.ingressController.namespaceSelector=null")
refuse("networkPolicy.dns needs a namespaceSelector",
       "--set", "desktop.image=x", "--set-json", "networkPolicy.dns={\"namespaceSelector\":null,\"podSelector\":null}")
refuse("SELKIES_[A-Z0-9_]+", "--set", "desktop.image=x", "--set-string", "desktop.selkies.selkies_x=1")
print("ok: invalid values refused")

sys.exit(1 if errors else 0)
