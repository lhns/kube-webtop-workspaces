// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/scheme"
)

var sweepNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

const sweepAfter = 72 * time.Hour

func sweepDesktop(name string, on bool, seen *time.Time) *v1alpha1.Desktop {
	d := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       v1alpha1.DesktopSpec{User: name, PowerState: v1alpha1.PowerOff},
	}
	if on {
		d.Spec.PowerState = v1alpha1.PowerOn
	}
	if seen != nil {
		t := metav1.NewTime(*seen)
		d.Spec.LastSeen = &t
	}
	return d
}

func gatewayDep(ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "ns"}, Status: appsv1.DeploymentStatus{ReadyReplicas: ready}}
}

func sweep(t *testing.T, after time.Duration, objs ...client.Object) (*UnseenSweep, client.Client) {
	t.Helper()
	sch, err := scheme.New()
	if err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithScheme(sch).
		WithStatusSubresource(&v1alpha1.Desktop{}).WithObjects(objs...).Build()
	return &UnseenSweep{Client: c, Namespace: "ns", Gateway: "gw", After: after, Now: func() time.Time { return sweepNow }}, c
}

func get(t *testing.T, c client.Client, name string) *v1alpha1.Desktop {
	t.Helper()
	d := &v1alpha1.Desktop{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: name}, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSweepStopsAtExpiryNotBefore(t *testing.T) {
	old, edge, fresh := sweepNow.Add(-sweepAfter-time.Minute), sweepNow.Add(-sweepAfter), sweepNow.Add(-time.Hour)
	s, c := sweep(t, sweepAfter, gatewayDep(1),
		sweepDesktop("old", true, &old), sweepDesktop("edge", true, &edge), sweepDesktop("fresh", true, &fresh),
		sweepDesktop("offold", false, &old))
	if err := s.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := get(t, c, "old"); d.Spec.PowerState != v1alpha1.PowerOff || d.Status.LastStop == nil || d.Status.LastStop.Reason != v1alpha1.StopUnseen {
		t.Fatalf("old: %+v %+v", d.Spec, d.Status.LastStop)
	}
	for _, n := range []string{"edge", "fresh"} {
		if get(t, c, n).Spec.PowerState != v1alpha1.PowerOn {
			t.Errorf("%s was stopped", n)
		}
	}
	if get(t, c, "offold").Status.LastStop != nil {
		t.Error("an Off desktop got a stop record")
	}
}

func TestSweepSkipsDuringGatewayOutage(t *testing.T) {
	old := sweepNow.Add(-10 * sweepAfter)
	for name, objs := range map[string][]client.Object{
		"no ready pod":  {gatewayDep(0), sweepDesktop("a", true, &old)},
		"no Deployment": {sweepDesktop("a", true, &old)},
	} {
		s, c := sweep(t, sweepAfter, objs...)
		if err := s.SweepOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if get(t, c, "a").Spec.PowerState != v1alpha1.PowerOn {
			t.Errorf("%s: stopped during an outage", name)
		}
	}
}

func TestSweepStampsMissingLastSeenAndNeverExpiresIt(t *testing.T) {
	s, c := sweep(t, sweepAfter, gatewayDep(1), sweepDesktop("a", true, nil))
	if err := s.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := get(t, c, "a")
	if d.Spec.PowerState != v1alpha1.PowerOn || d.Spec.LastSeen == nil || !d.Spec.LastSeen.Time.Equal(sweepNow) {
		t.Fatalf("spec %+v", d.Spec)
	}
}
