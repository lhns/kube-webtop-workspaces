// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

// leaky is what the kubelet reports: none of it may reach a page.
const leaky = `back-off restarting failed container on node worker-7 /var/lib/kubelet/pods/abc <script>alert(1)</script>`

func failed(stage string) dopt {
	return func(d *v1alpha1.Desktop) {
		d.Spec.PowerState = v1alpha1.PowerOn
		d.Status.Phase = v1alpha1.PhaseError
		d.Status.Stage = stage
		d.Status.Message = leaky
		d.Status.PodName = "desktop-alice-5d8f7-xk2lp"
	}
}

func csrfOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookie {
			return c.Value
		}
	}
	t.Fatalf("no csrf cookie: %v", rec.Result().Cookies())
	return ""
}

func postRecover(e *env, target, token string, hdr map[string]string) *httptest.ResponseRecorder {
	form := url.Values{csrfField: {token}}
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Remote-User", "alice")
	req.AddCookie(&http.Cookie{Name: csrfCookie, Value: token})
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestErrorPageStages(t *testing.T) {
	for stage, want := range map[string]string{
		"ErrImagePull":               "image couldn't be downloaded",
		"ImagePullBackOff":           "image couldn't be downloaded",
		"InvalidImageName":           "image couldn't be downloaded",
		"CrashLoopBackOff":           "keeps crashing",
		"CreateContainerConfigError": "container couldn't be set up",
		"CreateContainerError":       "container failed to start",
		"RunContainerError":          "container failed to start",
		"StartError":                 "container failed to start",
		"<b>unknown</b>":             "container failed to start",
	} {
		t.Run(stage, func(t *testing.T) {
			e := newEnv(t, nil, desktop("alice", "alice", failed(stage)))
			rec := e.do("GET", "/", navHeaders)
			body := rec.Body.String()
			if rec.Code != 200 || !strings.Contains(html.UnescapeString(body), "Your desktop couldn't start") || !strings.Contains(html.UnescapeString(body), want) ||
				rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("code %d body %s", rec.Code, body)
			}
			for _, s := range []string{`action="/.webtop/restart"`, `action="/.webtop/stop"`, "Restart desktop", "Stop desktop"} {
				if !strings.Contains(body, s) {
					t.Errorf("body lacks %q", s)
				}
			}
			// The form carries the cookie's token; nothing internal is shown or escapes its context.
			if tok := csrfOf(t, rec); strings.Count(body, `name="csrf" value="`+tok+`"`) != 2 {
				t.Errorf("csrf token not in both forms")
			}
			for _, s := range []string{"worker-7", "/var/lib", "alert(1)", "xk2lp", "<script>alert", "<b>unknown", stage + "<"} {
				if strings.Contains(body, s) {
					t.Errorf("body leaks %q", s)
				}
			}
			if counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": outFailed}) != 1 {
				t.Error("failed outcome not counted")
			}
		})
	}
}

// An invalid name or foreign volume needs an administrator: no buttons that cannot help.
func TestErrorPageSetupFailureHasNoButtons(t *testing.T) {
	e := newEnv(t, nil, desktop("alice", "alice", failed("Error")))
	body := e.do("GET", "/", navHeaders).Body.String()
	if !strings.Contains(body, "An administrator has to fix this") || strings.Contains(body, "<form") {
		t.Fatalf("body %s", body)
	}
	if rec := postRecover(e, "/.webtop/restart", "t", nil); rec.Code != 303 || e.desktop(t, "alice").Spec.RestartRequestedAt != nil {
		t.Errorf("restart acted: code %d", rec.Code)
	}
}

func TestErrorPageAdminURLs(t *testing.T) {
	e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" }, desktop("bob", "bob", failed("CrashLoopBackOff")))
	body := e.do("GET", "/u/bob/", navHeaders, map[string]string{"Remote-Groups": "admins"}).Body.String()
	for _, s := range []string{`action="/u/bob/.webtop/restart"`, `action="/u/bob/.webtop/stop"`, `EventSource("/u/bob/.webtop/events")`} {
		if !strings.Contains(body, s) {
			t.Errorf("body lacks %q: %s", s, body)
		}
	}
}

func TestRestartPost(t *testing.T) {
	const tok = "tok"
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

	t.Run("owner", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", failed("CrashLoopBackOff")))
		e.h.now = func() time.Time { return now }
		rec := postRecover(e, "/.webtop/restart", tok, nil)
		if rec.Code != 303 || rec.Header().Get("Location") != "/" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
		}
		d := e.desktop(t, "alice")
		if d.Spec.RestartRequestedAt == nil || !d.Spec.RestartRequestedAt.Time.Equal(now) || d.Spec.PowerState != v1alpha1.PowerOn {
			t.Fatalf("spec = %+v", d.Spec)
		}
		if counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": outRestart}) != 1 ||
			counter(t, e.reg, "webtop_gateway_starts_total", map[string]string{"trigger": triggerRestart}) != 1 {
			t.Error("restart not counted")
		}

		// The status still shows the old error: the boot page stands in, then the error page returns.
		nav := func() string { return e.do("GET", "/", navHeaders).Body.String() }
		if b := nav(); !strings.Contains(b, "Starting your desktop") || strings.Contains(b, "alert(1)") || strings.Contains(b, "worker-7") {
			t.Errorf("after restart: %s", b)
		}
		e.h.now = func() time.Time { return now.Add(restartGrace + time.Second) }
		if b := nav(); !strings.Contains(html.UnescapeString(b), "couldn't start") {
			t.Errorf("after grace: %s", b)
		}
	})
	t.Run("admin on another's desktop", func(t *testing.T) {
		e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" }, desktop("bob", "bob", failed("ErrImagePull")))
		rec := postRecover(e, "/u/bob/.webtop/restart", tok, map[string]string{"Remote-Groups": "admins"})
		if rec.Code != 303 || rec.Header().Get("Location") != "/u/bob/" || e.desktop(t, "bob").Spec.RestartRequestedAt == nil {
			t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("someone else's desktop is refused", func(t *testing.T) {
		e := newEnv(t, nil, desktop("bob", "bob", failed("ErrImagePull")))
		for _, p := range []string{"/u/bob/.webtop/restart", "/u/bob/.webtop/stop"} {
			if rec := postRecover(e, p, tok, nil); rec.Code != 403 {
				t.Errorf("%s: code %d", p, rec.Code)
			}
		}
		if d := e.desktop(t, "bob"); d.Spec.RestartRequestedAt != nil || d.Spec.PowerState != v1alpha1.PowerOn {
			t.Errorf("spec changed: %+v", d.Spec)
		}
	})
	t.Run("csrf failures", func(t *testing.T) {
		for name, tc := range map[string]struct {
			field string
			hdr   map[string]string
		}{
			"field mismatch": {"x", nil},
			"cross-site":     {tok, map[string]string{"Sec-Fetch-Site": "cross-site"}},
		} {
			e := newEnv(t, nil, desktop("alice", "alice", failed("CrashLoopBackOff")))
			form := url.Values{csrfField: {tc.field}}
			req := httptest.NewRequest("POST", "/.webtop/restart", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Remote-User", "alice")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.AddCookie(&http.Cookie{Name: csrfCookie, Value: tok})
			for k, v := range tc.hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != 403 || e.desktop(t, "alice").Spec.RestartRequestedAt != nil {
				t.Errorf("%s: code %d", name, rec.Code)
			}
		}
		req := httptest.NewRequest("POST", "/.webtop/stop", nil) // no cookie, no field
		req.Header.Set("Remote-User", "alice")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		e := newEnv(t, nil, desktop("alice", "alice", failed("CrashLoopBackOff")))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 403 || e.desktop(t, "alice").Spec.PowerState != v1alpha1.PowerOn {
			t.Errorf("no token: code %d", rec.Code)
		}
	})
	t.Run("a working desktop is not restarted by a stale page", func(t *testing.T) {
		b := htmlBackend(t, nil)
		e := newEnv(t, nil, desktop("alice", "alice", running(b.URL)))
		if rec := postRecover(e, "/.webtop/restart", tok, nil); rec.Code != 303 || e.desktop(t, "alice").Spec.RestartRequestedAt != nil {
			t.Errorf("code %d", rec.Code)
		}
	})
	t.Run("GET not allowed", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", failed("CrashLoopBackOff")))
		for _, p := range []string{"/.webtop/restart", "/.webtop/stop"} {
			if rec := e.do("GET", p); rec.Code != 405 {
				t.Errorf("%s: code %d", p, rec.Code)
			}
		}
	})
}

func TestStopPost(t *testing.T) {
	e := newEnv(t, nil, desktop("alice", "alice", failed("ImagePullBackOff")))
	rec := postRecover(e, "/.webtop/stop", "tok", nil)
	body := rec.Body.String()
	// The off page right away: a redirect could meet the state before the stop is recorded and start it again.
	if rec.Code != 200 || !strings.Contains(body, "Your desktop is off") || !strings.Contains(body, "failed to start") ||
		!strings.Contains(body, `action="/.webtop/start"`) {
		t.Fatalf("code %d body %s", rec.Code, body)
	}
	if d := e.desktop(t, "alice"); d.Spec.PowerState != v1alpha1.PowerOff || d.Spec.RestartRequestedAt != nil {
		t.Fatalf("spec = %+v", d.Spec)
	}
	if len(e.st.powerOns) != 0 {
		t.Error("stop started the desktop")
	}
	if counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": outStop}) != 1 {
		t.Error("stop not counted")
	}
}

// The boot page and its events never carry the kubelet's message for an Error.
func TestErrorStatusHidesMessage(t *testing.T) {
	d := desktop("alice", "alice", failed("CrashLoopBackOff"))
	ev := eventOf(d, time.Now(), false)
	if ev.Message != "" || ev.Stage != "Restarting" {
		t.Errorf("event = %+v", ev)
	}
	d.Status.Phase, d.Status.Message = v1alpha1.PhaseStarting, "unschedulable"
	if got := eventOf(d, time.Now(), false).Message; got != "unschedulable" {
		t.Errorf("message = %q", got)
	}
}
