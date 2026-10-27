// SPDX-License-Identifier: Apache-2.0

package powerapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

const (
	ns  = "webtop"
	sa  = "webtop-desktop"
	aud = "kube-webtop-workspaces"
)

type fakeAuth struct {
	status *authenticationv1.TokenReviewStatus
	err    error
}

func (f *fakeAuth) Review(context.Context, string) (*authenticationv1.TokenReviewStatus, error) {
	return f.status, f.err
}

func okStatus(podName, podUID string) *authenticationv1.TokenReviewStatus {
	return &authenticationv1.TokenReviewStatus{
		Authenticated: true,
		Audiences:     []string{aud},
		User: authenticationv1.UserInfo{
			Username: "system:serviceaccount:" + ns + ":" + sa,
			Extra: map[string]authenticationv1.ExtraValue{
				extraPodName: {podName}, extraPodUID: {podUID},
			},
		},
	}
}

type env struct {
	srv  *Server
	c    client.Client
	auth *fakeAuth
}

func desktopPod(uid string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "desktop-alice-abc", Namespace: ns, UID: types.UID(uid),
		Labels: names.Labels("alice"),
	}, Status: corev1.PodStatus{StartTime: &metav1.Time{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}}}
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	sch := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(sch)
	_ = v1alpha1.AddToScheme(sch)
	c := fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(&v1alpha1.Desktop{}).WithObjects(objs...).Build()
	a := &fakeAuth{status: okStatus("desktop-alice-abc", "pod-1")}
	return &env{c: c, auth: a, srv: &Server{
		Client: c, Reader: c, Auth: a, Namespace: ns, ServiceAccount: sa, Audience: aud,
		Now: func() time.Time { return time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC) },
	}}
}

func desktop(mod func(*v1alpha1.Desktop)) *v1alpha1.Desktop {
	d := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: ns},
		Spec:       v1alpha1.DesktopSpec{User: "alice", PowerState: v1alpha1.PowerOn},
		Status: v1alpha1.DesktopStatus{
			PodUID:    "pod-1",
			LastStart: ptrMicro(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		},
	}
	if mod != nil {
		mod(d)
	}
	return d
}

func ptrMicro(t time.Time) *metav1.MicroTime { m := metav1.NewMicroTime(t); return &m }

func (e *env) do(path, body string, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *env) power(body string) *httptest.ResponseRecorder {
	return e.do("/api/v1/power", body, "tok")
}

func (e *env) get(t *testing.T) *v1alpha1.Desktop {
	t.Helper()
	d := &v1alpha1.Desktop{}
	if err := e.c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "alice"}, d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAuthFailures(t *testing.T) {
	cases := []struct {
		name string
		mod  func(e *env)
		tok  string
		want int
	}{
		{"no token", nil, "", 401},
		{"unauthenticated", func(e *env) { e.auth.status = &authenticationv1.TokenReviewStatus{} }, "tok", 401},
		{"review error", func(e *env) { e.auth.err = errors.New("boom") }, "tok", 503},
		{"wrong audience", func(e *env) { e.auth.status.Audiences = []string{"other"} }, "tok", 403},
		{"wrong SA", func(e *env) { e.auth.status.User.Username = "system:serviceaccount:webtop:other" }, "tok", 403},
		{"stale token (uid mismatch)", func(e *env) { e.auth.status = okStatus("desktop-alice-abc", "old-uid") }, "tok", 403},
		{"unknown pod", func(e *env) { e.auth.status = okStatus("nope", "pod-1") }, "tok", 403},
		{"not pod bound", func(e *env) { e.auth.status.User.Extra = nil }, "tok", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, desktop(nil), desktopPod("pod-1"))
			if tc.mod != nil {
				tc.mod(e)
			}
			for _, p := range []string{"/api/v1/power", "/api/v1/report"} {
				if rec := e.do(p, `{"action":"stop","reason":"user"}`, tc.tok); rec.Code != tc.want {
					t.Errorf("%s: code %d, want %d", p, rec.Code, tc.want)
				}
			}
		})
	}
}

func TestUnmanagedPodForbidden(t *testing.T) {
	for _, drop := range []string{names.LabelManagedBy, names.LabelDesktop} {
		pod := desktopPod("pod-1")
		delete(pod.Labels, drop)
		e := newEnv(t, desktop(nil), pod)
		if rec := e.power(`{"action":"stop","reason":"user"}`); rec.Code != 403 {
			t.Errorf("without %s: %d", drop, rec.Code)
		}
		if e.get(t).Spec.PowerState != v1alpha1.PowerOn {
			t.Error("desktop must be untouched")
		}
	}
}

func TestPowerBadRequests(t *testing.T) {
	for _, body := range []string{
		`{"action":"stop","reason":"ldap"}`, `{"action":"stop","reason":"exit"}`, `{"action":"stop","reason":"admin"}`,
		`{"action":"stop","reason":""}`, `{"action":"explode","reason":"user"}`, `not json`,
	} {
		e := newEnv(t, desktop(nil), desktopPod("pod-1"))
		if rec := e.power(body); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
		d := e.get(t)
		if d.Spec.PowerState != v1alpha1.PowerOn || d.Status.LastStop != nil {
			t.Errorf("%s changed the desktop", body)
		}
	}
}

func TestStopRecordsLastStopThenOff(t *testing.T) {
	for _, action := range []string{"stop", "suspend"} {
		e := newEnv(t, desktop(nil), desktopPod("pod-1"))
		rec := e.power(`{"action":"` + action + `","reason":"idle"}`)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"desktop":"alice"`) {
			t.Fatalf("%s: %d %s", action, rec.Code, rec.Body)
		}
		d := e.get(t)
		if d.Spec.PowerState != v1alpha1.PowerOff {
			t.Fatal("powerState not Off")
		}
		if d.Status.LastStop == nil || d.Status.LastStop.Reason != v1alpha1.StopIdle ||
			d.Status.LastStop.Time.Before(d.Status.LastStart) {
			t.Fatalf("lastStop: %+v", d.Status.LastStop)
		}
	}
}

func TestStopIdempotent(t *testing.T) {
	e := newEnv(t, desktop(nil), desktopPod("pod-1"))
	e.power(`{"action":"stop","reason":"idle"}`)
	first := e.get(t).Status.LastStop.Time
	e.srv.Now = func() time.Time { return time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC) }
	if rec := e.power(`{"action":"stop","reason":"user"}`); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	d := e.get(t)
	if !d.Status.LastStop.Time.Equal(&first) || d.Status.LastStop.Reason != v1alpha1.StopIdle {
		t.Fatalf("repeat must not change lastStop: %+v", d.Status.LastStop)
	}
}

func TestStopRetryAfterPartialFailure(t *testing.T) {
	// lastStop already recorded but still On: only powerState is written.
	e := newEnv(t, desktop(func(d *v1alpha1.Desktop) {
		d.Status.LastStop = &v1alpha1.LastStop{Time: *ptrMicro(time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC)), Reason: v1alpha1.StopUser}
	}), desktopPod("pod-1"))
	e.power(`{"action":"stop","reason":"idle"}`)
	d := e.get(t)
	if d.Spec.PowerState != v1alpha1.PowerOff || d.Status.LastStop.Reason != v1alpha1.StopUser {
		t.Fatalf("%+v", d)
	}
}

func TestKubeStopChangesNothing(t *testing.T) {
	for _, action := range []string{"stop", "suspend", "reboot"} {
		e := newEnv(t, desktop(nil), desktopPod("pod-1"))
		if rec := e.power(`{"action":"` + action + `","reason":"kube-stop"}`); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
		d := e.get(t)
		if d.Spec.PowerState != v1alpha1.PowerOn || d.Status.LastStop != nil || d.Spec.RestartRequestedAt != nil {
			t.Fatalf("%s kube-stop changed the desktop: %+v", action, d)
		}
	}
}

func TestReboot(t *testing.T) {
	e := newEnv(t, desktop(nil), desktopPod("pod-1"))
	if rec := e.power(`{"action":"reboot","reason":"user"}`); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	d := e.get(t)
	if d.Spec.RestartRequestedAt == nil || !d.Spec.RestartRequestedAt.Time.Equal(time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("restartRequestedAt: %v", d.Spec.RestartRequestedAt)
	}
	if d.Spec.PowerState != v1alpha1.PowerOn || d.Status.LastStop != nil {
		t.Fatal("reboot must not stop")
	}
	// A request newer than the pod's start is not overwritten.
	e.srv.Now = func() time.Time { return time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC) }
	e.power(`{"action":"reboot","reason":"user"}`)
	if got := e.get(t).Spec.RestartRequestedAt; !got.Time.Equal(time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("restartRequestedAt moved to %v", got)
	}
}

func TestReport(t *testing.T) {
	e := newEnv(t, desktop(nil), desktopPod("pod-1"))
	body := `{"stage":"Starting Plasma","bootProgress":60,"connectedClients":2,"idleSeconds":12}`
	if rec := e.do("/api/v1/report", body, "tok"); rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body)
	}
	d := e.get(t)
	st := d.Status
	if st.Stage != "Starting Plasma" || st.BootProgress != 60 || st.ConnectedClients != 2 || st.IdleSeconds != 12 ||
		st.AgentReady || st.LastReportTime == nil {
		t.Fatalf("status: %+v", st)
	}
	if rec := e.do("/api/v1/report", `{"ready":true,"stage":"Ready","bootProgress":100}`, "tok"); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	if !e.get(t).Status.AgentReady {
		t.Fatal("agentReady not set")
	}
	// After ready, stage/bootProgress are ignored but the rest is applied.
	e.do("/api/v1/report", `{"stage":"late","bootProgress":10,"connectedClients":0}`, "tok")
	st = e.get(t).Status
	if st.Stage == "late" || st.BootProgress == 10 || st.ConnectedClients != 0 {
		t.Fatalf("status after ready: %+v", st)
	}
}

func TestReportFromOldPodConflicts(t *testing.T) {
	e := newEnv(t, desktop(func(d *v1alpha1.Desktop) { d.Status.PodUID = "pod-2" }), desktopPod("pod-1"))
	if rec := e.do("/api/v1/report", `{"ready":true}`, "tok"); rec.Code != 409 {
		t.Fatal(rec.Code)
	}
	if e.get(t).Status.AgentReady {
		t.Fatal("report must be ignored")
	}
}

func TestReportBadJSON(t *testing.T) {
	e := newEnv(t, desktop(nil), desktopPod("pod-1"))
	if rec := e.do("/api/v1/report", `{`, "tok"); rec.Code != 400 {
		t.Fatal(rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
}
