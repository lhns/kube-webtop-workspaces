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
    if name == "all-on":
        for k in ("Ingress", "HTTPRoute", "ServiceMonitor"):
            if k not in kinds:
                fail(f"{name}: {k} not rendered")
    elif any(k in kinds for k in ("Ingress", "HTTPRoute", "ServiceMonitor")):
        fail(f"{name}: optional resources rendered by default")

    for d in docs:
        if d["kind"] == "Deployment":
            labels = d["spec"]["template"]["metadata"]["labels"]
            sel = d["spec"]["selector"]["matchLabels"]
            if any(labels.get(k) != v for k, v in sel.items()):
                fail(f"{name}: selector of {d['metadata']['name']} does not match its pod labels")
    print(f"ok: {name} ({len(docs)} objects)")

# 3. An empty ingress-controller selector must be refused.
bad = subprocess.run(
    ["helm", "template", "r", str(CHART), "--kube-version", KUBE_VERSION, "--set", "desktop.image=x",
     "--set-json", "networkPolicy.ingressController={\"namespaceSelector\":null,\"podSelector\":null}"],
    capture_output=True, text=True)
if bad.returncode == 0:
    fail("empty networkPolicy.ingressController was accepted")

sys.exit(1 if errors else 0)
