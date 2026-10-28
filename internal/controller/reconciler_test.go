// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/metrics"
)

func startSamples(t *testing.T) uint64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.StartDuration.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

// A reconcile on a cache that has not seen our Running write yet must not observe the start again.
func TestStartDurationObservedOnce(t *testing.T) {
	scheme, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte("namespace: ns\n"))
	if err != nil {
		t.Fatal(err)
	}
	start := metav1.NewMicroTime(time.Now().Add(-time.Minute))
	stale := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
		Spec:       v1alpha1.DesktopSpec{User: "a", PowerState: v1alpha1.PowerOn},
		Status:     v1alpha1.DesktopStatus{Phase: v1alpha1.PhaseStarting, PodUID: "p1", AgentReady: true, LastStart: &start},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Desktop{}).WithObjects(stale.DeepCopy()).Build()
	r := &Reconciler{Client: c}
	r.SetConfig(cfg)
	pod := mkPod(func(p *corev1.Pod) { containerState(running())(p); ready(p) })
	pod.UID = "p1"

	before := startSamples(t)
	for range 2 {
		d := stale.DeepCopy()
		if err := r.finish(context.Background(), d, d.DeepCopy(), []corev1.Pod{*pod}, pod, finishArgs{on: true, want: 1}); err != nil {
			t.Fatal(err)
		}
		if d.Status.Phase != v1alpha1.PhaseRunning {
			t.Fatalf("phase %s, want Running", d.Status.Phase)
		}
	}
	if n := startSamples(t) - before; n != 1 {
		t.Errorf("start duration observed %d times, want 1", n)
	}
}
