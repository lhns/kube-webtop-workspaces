// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The controller must never be able to delete a PVC (docs/contract.md, section 3.1).
func TestRBACNeverDeletesPVCs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "controller", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) == 0 {
		t.Fatal("role has no rules")
	}
	for _, rule := range role.Rules {
		coversPVC := slices.Contains(rule.Resources, "persistentvolumeclaims") || slices.Contains(rule.Resources, "*")
		coversCore := slices.Contains(rule.APIGroups, "") || slices.Contains(rule.APIGroups, "*")
		if !coversPVC || !coversCore {
			continue
		}
		for _, v := range rule.Verbs {
			if v == "delete" || v == "deletecollection" || v == "*" {
				t.Errorf("rule %+v lets the controller %s PVCs", rule, v)
			}
		}
	}
}

// The reconciler reads and watches pod events for the boot stages (docs/contract.md, section 3.4).
func TestRBACReadsEvents(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "controller", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, rule := range role.Rules {
		if slices.Contains(rule.Resources, "events") && slices.Contains(rule.APIGroups, "") {
			for _, v := range rule.Verbs {
				got[v] = true
			}
		}
	}
	for _, v := range []string{"list", "watch"} {
		if !got[v] {
			t.Errorf("controller role cannot %s events", v)
		}
	}
	for _, v := range []string{"delete", "deletecollection", "update", "*"} {
		if got[v] {
			t.Errorf("controller role may %s events", v)
		}
	}
}
