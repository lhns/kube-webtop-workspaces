// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

const pulledMsg = `Successfully pulled image "ghcr.io/x@sha256:abc" in 9m13.252s (9m13.252s including waiting). Image size: 3745009478 bytes.`

var pulledWant = pulled{Image: "ghcr.io/x@sha256:abc", Size: 3745009478, Duration: 9*time.Minute + 13*time.Second + 252*time.Millisecond}

func TestParsePulled(t *testing.T) {
	if p := parsePulled(pulledMsg); p == nil || *p != pulledWant {
		t.Fatalf("got %+v", p)
	}
	if p := parsePulled(`Successfully pulled image "i" in 2.5s (2.5s including waiting)`); p == nil || p.Size != 0 || p.Duration != 2500*time.Millisecond {
		t.Errorf("older kubelet without size: %+v", p)
	}
	for _, msg := range []string{`Container image "ghcr.io/x" already present on machine`, "", `Successfully pulled image "i" in soon`} {
		if p := parsePulled(msg); p != nil {
			t.Errorf("parsePulled(%q) = %+v, want nil", msg, p)
		}
	}
}

func ev(reason, msg string, at time.Time) corev1.Event {
	return corev1.Event{Reason: reason, Message: msg, LastTimestamp: metav1.NewTime(at)}
}

func TestSummarizeEvents(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	pulling := ev("Pulling", `Pulling image "ghcr.io/x@sha256:abc"`, at(10))
	tests := []struct {
		name   string
		events []corev1.Event
		want   podEvents
	}{
		{"none", nil, podEvents{}},
		{"attach failing", []corev1.Event{ev("FailedAttachVolume", "x", at(1))}, podEvents{AttachFailing: true}},
		{"attach recovered", []corev1.Event{ev("SuccessfulAttachVolume", "", at(5)), ev("FailedAttachVolume", "x", at(1))}, podEvents{}},
		{"attach failed again after success", []corev1.Event{ev("SuccessfulAttachVolume", "", at(5)), ev("FailedAttachVolume", "x", at(9))}, podEvents{AttachFailing: true}},
		{"attach success in the same second ends it", []corev1.Event{ev("FailedAttachVolume", "x", at(5)), ev("SuccessfulAttachVolume", "", at(5))}, podEvents{}},
		{"pulling", []corev1.Event{pulling},
			podEvents{Pulling: true, PullImage: "ghcr.io/x@sha256:abc", PullStarted: at(10)}},
		{"pulled ends pulling", []corev1.Event{pulling, ev("Pulled", pulledMsg, at(20))}, podEvents{Pulled: &pulledWant}},
		{"already present is no pull", []corev1.Event{ev("Pulled", `Container image "i" already present on machine`, at(3))}, podEvents{}},
		{"pulling after an old pull", []corev1.Event{ev("Pulled", pulledMsg, at(1)), pulling},
			podEvents{Pulling: true, PullImage: "ghcr.io/x@sha256:abc", PullStarted: at(10), Pulled: &pulledWant}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeEvents(tc.events)
			if (got.Pulled == nil) != (tc.want.Pulled == nil) || (got.Pulled != nil && *got.Pulled != *tc.want.Pulled) {
				t.Fatalf("pulled: got %+v want %+v", got.Pulled, tc.want.Pulled)
			}
			got.Pulled, tc.want.Pulled = nil, nil
			if got != tc.want {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestEventTimeFallbacks(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := corev1.Event{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now)}}
	if !eventTime(&e).Equal(now) {
		t.Error("creation fallback")
	}
	e.FirstTimestamp = metav1.NewTime(now.Add(time.Second))
	if !eventTime(&e).Equal(now.Add(time.Second)) {
		t.Error("first fallback")
	}
	e.LastTimestamp = metav1.NewTime(now.Add(2 * time.Second))
	if !eventTime(&e).Equal(now.Add(2 * time.Second)) {
		t.Error("last wins")
	}
}

func TestUpdateImagePull(t *testing.T) {
	const img = "ghcr.io/x@sha256:abc"
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	pull := podEvents{Pulling: true, PullImage: img, PullStarted: started}
	done := podEvents{Pulled: &pulled{Image: img, Size: 100, Duration: 90*time.Second + 400*time.Millisecond}}

	got := updateImagePull(nil, img, pull)
	if got.Image != img || got.StartedAt == nil || !got.StartedAt.Time.Equal(started) || got.LastDurationSeconds != 0 {
		t.Fatalf("pulling: %+v", got)
	}
	got = updateImagePull(got, img, done)
	if got.StartedAt != nil || got.LastSizeBytes != 100 || got.LastDurationSeconds != 90 {
		t.Fatalf("done: %+v", got)
	}
	// Events are gone later: the record stays.
	if kept := updateImagePull(got, img, podEvents{}); *kept != *got {
		t.Fatalf("kept: %+v", kept)
	}
	// Another image ref: the old numbers must not describe it.
	if other := updateImagePull(got, "ghcr.io/x@sha256:def", podEvents{}); other.LastDurationSeconds != 0 || other.LastSizeBytes != 0 || other.Image != "ghcr.io/x@sha256:def" {
		t.Fatalf("other image: %+v", other)
	}
	// A Pulled for a different ref (an init container, say) is ignored.
	if o := updateImagePull(nil, img, podEvents{Pulled: &pulled{Image: "other", Duration: time.Minute}}); o.LastDurationSeconds != 0 {
		t.Fatalf("foreign pulled: %+v", o)
	}
	if updateImagePull(got, "", podEvents{}) != got {
		t.Error("no image keeps the record")
	}
}

func TestNextImagePullUsesOtherDesktops(t *testing.T) {
	const img = "ghcr.io/x@sha256:abc"
	scheme, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, ip *v1alpha1.ImagePull) *v1alpha1.Desktop {
		return &v1alpha1.Desktop{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}, Status: v1alpha1.DesktopStatus{ImagePull: ip}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		mk("other-image", &v1alpha1.ImagePull{Image: "different", LastDurationSeconds: 5, LastSizeBytes: 1}),
		mk("no-duration", &v1alpha1.ImagePull{Image: img}),
		mk("known", &v1alpha1.ImagePull{Image: img, LastDurationSeconds: 553, LastSizeBytes: 3745009478}),
	).Build()
	r := &Reconciler{Client: c}
	pull := podEvents{Pulling: true, PullImage: img, PullStarted: time.Now()}

	got := r.nextImagePull(context.Background(), "ns", nil, img, pull, true)
	if got.LastDurationSeconds != 553 || got.LastSizeBytes != 3745009478 || got.StartedAt == nil {
		t.Fatalf("borrowed: %+v", got)
	}
	// Not pulling: no lookup, nothing borrowed.
	if got := r.nextImagePull(context.Background(), "ns", nil, img, podEvents{}, false); got.LastDurationSeconds != 0 {
		t.Fatalf("not pulling: %+v", got)
	}
	// Its own record wins over other desktops'.
	own := &v1alpha1.ImagePull{Image: img, LastDurationSeconds: 60, LastSizeBytes: 7}
	if got := r.nextImagePull(context.Background(), "ns", own, img, pull, true); got.LastDurationSeconds != 60 {
		t.Fatalf("own: %+v", got)
	}
}
