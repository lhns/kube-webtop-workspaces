// SPDX-License-Identifier: Apache-2.0

// Package powerapi is the power/report HTTP API the in-pod agent talks to (docs/contract.md,
// section 8). It only writes Desktops; it never touches Deployments.
package powerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/metrics"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=webtop.lhns.github.io,resources=desktops,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=webtop.lhns.github.io,resources=desktops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

const (
	extraPodName = "authentication.kubernetes.io/pod-name"
	extraPodUID  = "authentication.kubernetes.io/pod-uid"
	maxBody      = 16 << 10
)

// Authenticator validates a bearer token.
type Authenticator interface {
	Review(ctx context.Context, token string) (*authenticationv1.TokenReviewStatus, error)
}

// TokenReviewer is the Kubernetes TokenReview Authenticator.
type TokenReviewer struct {
	Client   client.Client
	Audience string
}

func (t *TokenReviewer) Review(ctx context.Context, token string) (*authenticationv1.TokenReviewStatus, error) {
	tr := &authenticationv1.TokenReview{Spec: authenticationv1.TokenReviewSpec{
		Token: token, Audiences: []string{t.Audience},
	}}
	if err := t.Client.Create(ctx, tr); err != nil {
		return nil, err
	}
	return &tr.Status, nil
}

// Server implements the API.
type Server struct {
	// Client is the (cached) client; Reader is the uncached fallback for objects the cache
	// has not seen yet or has stale.
	Client         client.Client
	Reader         client.Reader
	Auth           Authenticator
	Namespace      string
	ServiceAccount string
	Audience       string
	Now            func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /api/v1/power", s.power)
	mux.HandleFunc("POST /api/v1/report", s.report)
	return mux
}

// httpError carries a status code out of the helpers.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func fail(code int, format string, a ...any) *httpError {
	return &httpError{code: code, msg: fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *httpError) {
	writeJSON(w, e.code, map[string]string{"error": e.msg})
}

// caller is an authenticated desktop pod.
type caller struct {
	pod     *corev1.Pod
	desktop string
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func first(v authenticationv1.ExtraValue) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (s *Server) authenticate(ctx context.Context, r *http.Request) (*caller, *httpError) {
	token := bearer(r)
	if token == "" {
		return nil, fail(http.StatusUnauthorized, "missing bearer token")
	}
	st, err := s.Auth.Review(ctx, token)
	if err != nil {
		slog.Error("tokenreview failed", "err", err)
		return nil, fail(http.StatusServiceUnavailable, "token review failed")
	}
	if !st.Authenticated {
		return nil, fail(http.StatusUnauthorized, "invalid token")
	}
	audOK := false
	for _, a := range st.Audiences {
		audOK = audOK || a == s.Audience
	}
	if !audOK {
		return nil, fail(http.StatusForbidden, "wrong audience")
	}
	if want := fmt.Sprintf("system:serviceaccount:%s:%s", s.Namespace, s.ServiceAccount); st.User.Username != want {
		return nil, fail(http.StatusForbidden, "wrong service account")
	}
	podName, podUID := first(st.User.Extra[extraPodName]), first(st.User.Extra[extraPodUID])
	if podName == "" || podUID == "" {
		return nil, fail(http.StatusForbidden, "token is not bound to a pod")
	}
	pod, herr := s.livePod(ctx, podName, types.UID(podUID))
	if herr != nil {
		return nil, herr
	}
	desktop := pod.Labels[names.LabelDesktop]
	if pod.Labels[names.LabelManagedBy] != names.ManagedBy || desktop == "" {
		return nil, fail(http.StatusForbidden, "pod is not a managed desktop pod")
	}
	return &caller{pod: pod, desktop: desktop}, nil
}

// livePod finds the pod of that name and UID: cache first, then the API on a miss or mismatch.
func (s *Server) livePod(ctx context.Context, name string, uid types.UID) (*corev1.Pod, *httpError) {
	key := client.ObjectKey{Namespace: s.Namespace, Name: name}
	for _, rd := range []client.Reader{s.Client, s.Reader} {
		if rd == nil {
			continue
		}
		pod := &corev1.Pod{}
		err := rd.Get(ctx, key, pod)
		switch {
		case err == nil && pod.UID == uid:
			return pod, nil
		case err == nil || apierrors.IsNotFound(err):
			continue
		default:
			return nil, fail(http.StatusServiceUnavailable, "pod lookup failed")
		}
	}
	return nil, fail(http.StatusForbidden, "no such pod")
}

// getDesktop reads cache-first, or API-first when fresh: a power request decides from lastStart
// whether a stop is already recorded, and a lagging cache would hide a new start.
func (s *Server) getDesktop(ctx context.Context, name string, fresh bool) (*v1alpha1.Desktop, *httpError) {
	key := client.ObjectKey{Namespace: s.Namespace, Name: name}
	readers := []client.Reader{s.Client, s.Reader}
	if fresh {
		readers[0], readers[1] = s.Reader, s.Client
	}
	for _, rd := range readers {
		if rd == nil {
			continue
		}
		d := &v1alpha1.Desktop{}
		err := rd.Get(ctx, key, d)
		switch {
		case err == nil:
			return d, nil
		case apierrors.IsNotFound(err):
			continue
		default:
			return nil, fail(http.StatusServiceUnavailable, "desktop lookup failed")
		}
	}
	return nil, fail(http.StatusForbidden, "no such desktop")
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// ---- power ----

type powerRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

var (
	powerActions = map[string]bool{"stop": true, "suspend": true, "reboot": true}
	agentReasons = map[string]bool{"user": true, "idle": true, "suspend": true, "kube-stop": true}
)

// label keeps arbitrary client input out of metric labels.
func label(v string, allowed map[string]bool) string {
	if allowed[v] || v == "" {
		return v
	}
	return "invalid"
}

func (s *Server) power(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
	var req powerRequest
	defer func() {
		metrics.PowerRequests.WithLabelValues(label(req.Action, powerActions),
			label(req.Reason, agentReasons), strconv.Itoa(sw.code)).Inc()
	}()

	c, herr := s.authenticate(r.Context(), r)
	if herr != nil {
		writeErr(sw, herr)
		return
	}
	if err := json.NewDecoder(http.MaxBytesReader(sw, r.Body, maxBody)).Decode(&req); err != nil {
		writeErr(sw, fail(http.StatusBadRequest, "invalid JSON body"))
		return
	}
	if !powerActions[req.Action] {
		writeErr(sw, fail(http.StatusBadRequest, "action must be stop, suspend or reboot"))
		return
	}
	if !agentReasons[req.Reason] && !(req.Action == "reboot" && req.Reason == "") {
		writeErr(sw, fail(http.StatusBadRequest, "reason must be user, idle, suspend or kube-stop"))
		return
	}
	if req.Reason != "kube-stop" {
		if herr := s.applyPower(r.Context(), c, req); herr != nil {
			writeErr(sw, herr)
			return
		}
	}
	writeJSON(sw, http.StatusOK, map[string]string{"desktop": c.desktop, "action": req.Action, "reason": req.Reason})
}

func (s *Server) applyPower(ctx context.Context, c *caller, req powerRequest) *httpError {
	d, herr := s.getDesktop(ctx, c.desktop, true)
	if herr != nil {
		return herr
	}
	if req.Action == "reboot" {
		return s.reboot(ctx, c, d)
	}
	return s.stop(ctx, d, v1alpha1.StopReason(req.Reason))
}

// stop records lastStop first, then powers off, so the stop is on record before the pod exits.
func (s *Server) stop(ctx context.Context, d *v1alpha1.Desktop, reason v1alpha1.StopReason) *httpError {
	recorded := d.Status.LastStop != nil &&
		(d.Status.LastStart == nil || !d.Status.LastStop.Time.Before(d.Status.LastStart))
	if recorded && d.Spec.PowerState == v1alpha1.PowerOff {
		return nil
	}
	if !recorded {
		orig := d.DeepCopy()
		d.Status.LastStop = &v1alpha1.LastStop{Time: metav1.NewMicroTime(s.now()), Reason: reason}
		if err := s.Client.Status().Patch(ctx, d, client.MergeFrom(orig)); err != nil {
			slog.Error("record lastStop", "desktop", d.Name, "err", err)
			return fail(http.StatusServiceUnavailable, "could not record the stop")
		}
		metrics.StopsTotal.WithLabelValues(string(reason)).Inc()
	}
	orig := d.DeepCopy()
	d.Spec.PowerState = v1alpha1.PowerOff
	if err := s.Client.Patch(ctx, d, client.MergeFrom(orig)); err != nil {
		slog.Error("set powerState Off", "desktop", d.Name, "err", err)
		return fail(http.StatusServiceUnavailable, "could not power off")
	}
	return nil
}

func (s *Server) reboot(ctx context.Context, c *caller, d *v1alpha1.Desktop) *httpError {
	if rr, st := d.Spec.RestartRequestedAt, c.pod.Status.StartTime; rr != nil && st != nil && rr.After(st.Time) {
		return nil // already requested for this pod
	}
	orig := d.DeepCopy()
	now := metav1.NewTime(s.now())
	d.Spec.RestartRequestedAt = &now
	if err := s.Client.Patch(ctx, d, client.MergeFrom(orig)); err != nil {
		slog.Error("request restart", "desktop", d.Name, "err", err)
		return fail(http.StatusServiceUnavailable, "could not request the restart")
	}
	return nil
}

// ---- report ----

type reportRequest struct {
	Stage            *string `json:"stage"`
	BootProgress     *int32  `json:"bootProgress"`
	Ready            *bool   `json:"ready"`
	ConnectedClients *int32  `json:"connectedClients"`
	IdleSeconds      *int64  `json:"idleSeconds"`
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w, code: http.StatusNoContent}
	defer func() { metrics.ReportRequests.WithLabelValues(strconv.Itoa(sw.code)).Inc() }()

	c, herr := s.authenticate(r.Context(), r)
	if herr != nil {
		writeErr(sw, herr)
		return
	}
	var req reportRequest
	if err := json.NewDecoder(http.MaxBytesReader(sw, r.Body, maxBody)).Decode(&req); err != nil {
		writeErr(sw, fail(http.StatusBadRequest, "invalid JSON body"))
		return
	}
	if req.BootProgress != nil && (*req.BootProgress < 0 || *req.BootProgress > 100) ||
		req.ConnectedClients != nil && *req.ConnectedClients < 0 ||
		req.IdleSeconds != nil && *req.IdleSeconds < 0 {
		writeErr(sw, fail(http.StatusBadRequest, "value out of range"))
		return
	}
	d, herr := s.getDesktop(r.Context(), c.desktop, false)
	if herr != nil {
		writeErr(sw, herr)
		return
	}
	if string(c.pod.UID) != d.Status.PodUID {
		writeErr(sw, fail(http.StatusConflict, "report from a pod that is not the current one"))
		return
	}
	orig := d.DeepCopy()
	st := &d.Status
	if !st.AgentReady {
		if req.Stage != nil {
			st.Stage = *req.Stage
		}
		if req.BootProgress != nil {
			st.BootProgress = *req.BootProgress
		}
	}
	if req.Ready != nil && *req.Ready {
		st.AgentReady = true
	}
	if req.ConnectedClients != nil {
		st.ConnectedClients = *req.ConnectedClients
	}
	if req.IdleSeconds != nil {
		st.IdleSeconds = *req.IdleSeconds
	}
	now := metav1.NewTime(s.now())
	st.LastReportTime = &now
	if err := s.Client.Status().Patch(r.Context(), d, client.MergeFrom(orig)); err != nil {
		slog.Error("apply report", "desktop", d.Name, "err", err)
		writeErr(sw, fail(http.StatusServiceUnavailable, "could not store the report"))
		return
	}
	sw.WriteHeader(http.StatusNoContent)
}

// Runnable serves the API as a manager Runnable on every replica.
type Runnable struct {
	Addr    string
	Handler http.Handler
}

func (Runnable) NeedLeaderElection() bool { return false }

func (r Runnable) Start(ctx context.Context) error {
	srv := &http.Server{Addr: r.Addr, Handler: r.Handler, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}
