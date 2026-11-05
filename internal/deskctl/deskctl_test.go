// SPDX-License-Identifier: Apache-2.0

package deskctl

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/render"
	"github.com/lhns/kube-webtop-workspaces/internal/scheme"
)

func setup(t *testing.T) (*CLI, client.Client, *bytes.Buffer, *corev1.PersistentVolumeClaim) {
	t.Helper()
	cfg, err := config.Parse([]byte("namespace: ns\n"))
	if err != nil {
		t.Fatal(err)
	}
	sch, err := scheme.New()
	if err != nil {
		t.Fatal(err)
	}
	seen := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	d := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "ns"},
		Spec:       v1alpha1.DesktopSpec{User: "alice", PowerState: v1alpha1.PowerOn, LastSeen: &seen},
		Status:     v1alpha1.DesktopStatus{Phase: v1alpha1.PhaseRunning, ConnectedClients: 2},
	}
	pvc := render.PVC(cfg, d)
	c := fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(&v1alpha1.Desktop{}).WithObjects(d, pvc).Build()
	out := &bytes.Buffer{}
	return &CLI{Client: c, Config: cfg, Out: out}, c, out, pvc
}

func TestList(t *testing.T) {
	cli, _, out, pvc := setup(t)
	if err := cli.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alice", "On", "Running", "2026-01-02T03:04:05Z", pvc.Name} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
}

func TestStop(t *testing.T) {
	cli, c, _, _ := setup(t)
	if err := cli.Stop(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	d := &v1alpha1.Desktop{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "alice"}, d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.PowerState != v1alpha1.PowerOff || d.Status.LastStop == nil || d.Status.LastStop.Reason != v1alpha1.StopAdmin {
		t.Fatalf("%+v %+v", d.Spec, d.Status.LastStop)
	}
	if err := cli.Stop(context.Background(), "nobody"); err == nil {
		t.Fatal("unknown user must fail")
	}
}

func TestRemoveKeepsThePVC(t *testing.T) {
	cli, c, out, pvc := setup(t)
	if err := cli.Remove(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "alice"}, &v1alpha1.Desktop{}); !apierrors.IsNotFound(err) {
		t.Fatalf("desktop still there: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("PVC gone: %v", err)
	}
	if !strings.Contains(out.String(), pvc.Name) || !strings.Contains(out.String(), "new desktop on the next visit") {
		t.Fatalf("output: %s", out)
	}
}
