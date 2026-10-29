// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

type staticDir map[string]struct{}

func (d staticDir) Members(context.Context) (map[string]struct{}, error) { return d, nil }
func (d staticDir) IsMember(_ context.Context, u string) (bool, error) {
	_, ok := d[u]
	return ok, nil
}

// The sync reads the member set while a reconcile may Add to the cache (run with -race).
func TestSyncDoesNotShareMemberMap(t *testing.T) {
	scheme, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Desktop{})
	for _, u := range []string{"a", "b", "c", "d"} {
		b = b.WithObjects(&v1alpha1.Desktop{ObjectMeta: metav1.ObjectMeta{Name: u, Namespace: "ns"}, Spec: v1alpha1.DesktopSpec{User: u}})
	}
	members := staticDir{"a": {}, "b": {}, "c": {}, "d": {}}
	cache := &MemberCache{}
	s := &LDAPSync{Client: b.Build(), Dir: members, Cache: cache, Namespace: "ns"}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			cache.Add("x")
		}
	}()
	for range 50 {
		if err := s.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if _, ok := members["x"]; ok {
		t.Error("cache.Add wrote into the directory's member map")
	}
}

// The directory's member set already includes the admin group (ldap.adminGroupDN): such a user
// keeps a running desktop, and only a user in neither group is stopped.
func TestSyncKeepsAdminGroupMember(t *testing.T) {
	scheme, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	b := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Desktop{})
	for _, u := range []string{"admin", "gone"} {
		b = b.WithObjects(&v1alpha1.Desktop{ObjectMeta: metav1.ObjectMeta{Name: u, Namespace: "ns"},
			Spec: v1alpha1.DesktopSpec{User: u, PowerState: v1alpha1.PowerOn}})
	}
	c := b.Build()
	s := &LDAPSync{Client: c, Dir: staticDir{"admin": {}, "other": {}}, Cache: &MemberCache{}, Namespace: "ns"}
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for u, want := range map[string]v1alpha1.PowerState{"admin": v1alpha1.PowerOn, "gone": v1alpha1.PowerOff} {
		var d v1alpha1.Desktop
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: u}, &d); err != nil {
			t.Fatal(err)
		}
		if d.Spec.PowerState != want {
			t.Errorf("%s: powerState %q, want %q", u, d.Spec.PowerState, want)
		}
	}
}
