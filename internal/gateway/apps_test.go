// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

type fakeAgent struct {
	calls []string // "<base host> <app>"
	err   error
}

func (f *fakeAgent) Open(_ context.Context, base *url.URL, app string) error {
	f.calls = append(f.calls, base.Host+" "+app)
	return f.err
}

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"/apps/org.kde.dolphin": "org.kde.dolphin",
		"/apps/firefox":         "firefox",
		"/apps/a_b-c.1":         "a_b-c.1",
		"/apps/":                "",
		"/apps":                 "",
		"/apps/a/b":             "",
		"/apps/a b":             "",
		"/apps/a%20b":           "",
		"/apps/x;y":             "",
		"/app/x":                "",
		"/x/apps/y":             "",
	} {
		got, ok := appName(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q %v", in, got, ok)
		}
	}
}

func TestAppRoute(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("backend " + r.URL.Path))
	}))
	defer backend.Close()
	admin := func(g *config.Gateway) { g.AdminGroup = "admins" }
	adminHdr := map[string]string{"Remote-Groups": "admins"}
	nav := func(h ...map[string]string) []map[string]string { return append([]map[string]string{navHeaders}, h...) }

	t.Run("own desktop redirects to the display2 page", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
		ag := &fakeAgent{}
		e.h.agent = ag
		rec := e.do("GET", "/apps/org.kde.dolphin", nav()...)
		if rec.Code != 303 || rec.Header().Get("Location") != "/#display2-right" {
			t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
		}
		if len(ag.calls) != 1 || ag.calls[0] != strings.TrimPrefix(backend.URL, "http://")+" org.kde.dolphin" {
			t.Fatalf("agent calls %v", ag.calls)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("redirect must not be cached")
		}
		if counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": outApp}) != 1 {
			t.Error("app request not counted")
		}
	})
	t.Run("admin prefix", func(t *testing.T) {
		e := newEnv(t, admin, desktop("bob", "bob", running(backend.URL)))
		ag := &fakeAgent{}
		e.h.agent = ag
		rec := e.do("GET", "/u/bob/apps/firefox", nav(adminHdr)...)
		if rec.Code != 303 || rec.Header().Get("Location") != "/u/bob/#display2-right" {
			t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
		}
		if len(ag.calls) != 1 {
			t.Fatalf("agent calls %v", ag.calls)
		}
	})
	t.Run("another user may not", func(t *testing.T) {
		e := newEnv(t, nil, desktop("bob", "bob", running(backend.URL)))
		ag := &fakeAgent{}
		e.h.agent = ag
		if rec := e.do("GET", "/u/bob/apps/firefox", nav()...); rec.Code != 403 || len(ag.calls) != 0 {
			t.Fatalf("code %d, agent calls %v", rec.Code, ag.calls)
		}
	})
	t.Run("only top-level navigation launches", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
		ag := &fakeAgent{}
		e.h.agent = ag
		for name, c := range map[string]struct {
			method string
			hdr    []map[string]string
		}{
			"fetch":   {"GET", []map[string]string{{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}}},
			"iframe":  {"GET", []map[string]string{{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}}},
			"image":   {"GET", []map[string]string{{"Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"}}},
			"post":    {"POST", nav()},
			"no hint": {"GET", nil},
		} {
			rec := e.do(c.method, "/apps/firefox", c.hdr...)
			if rec.Code == 303 || len(ag.calls) != 0 {
				t.Errorf("%s: code %d, agent calls %v", name, rec.Code, ag.calls)
			}
		}
		// A non-navigation request goes to the desktop as any other path.
		if rec := e.do("GET", "/apps/firefox", map[string]string{"Sec-Fetch-Mode": "cors"}); rec.Body.String() != "backend /apps/firefox" {
			t.Errorf("body %q", rec.Body)
		}
	})
	t.Run("invalid names go to the desktop", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
		ag := &fakeAgent{}
		e.h.agent = ag
		for _, p := range []string{"/apps/", "/apps/a/b", "/apps/a%20b", "/apps/x;y"} {
			if rec := e.do("GET", p, nav()...); rec.Code == 303 || len(ag.calls) != 0 {
				t.Errorf("%s: code %d, agent calls %v", p, rec.Code, ag.calls)
			}
		}
	})
	t.Run("unknown app", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
		e.h.agent = &fakeAgent{err: ErrAppNotFound}
		rec := e.do("GET", "/apps/nope", nav()...)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), "App not found") {
			t.Fatalf("code %d body %s", rec.Code, rec.Body)
		}
	})
	t.Run("agent unreachable", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(backend.URL)))
		e.h.agent = &fakeAgent{err: errors.New("connection refused")}
		rec := e.do("GET", "/apps/firefox", nav()...)
		if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("code %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
		}
	})
}

func TestAppStoppedDesktopBootsAndReturns(t *testing.T) {
	ag := &fakeAgent{}

	t.Run("off desktop under power on shows boot", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", powered(v1alpha1.PowerOff)))
		e.h.agent = ag
		rec := e.do("GET", "/apps/firefox", navHeaders)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Starting your desktop") {
			t.Fatalf("code %d body %s", rec.Code, rec.Body)
		}
		if len(e.st.powerOns) != 1 || len(ag.calls) != 0 {
			t.Fatalf("power-ons %v, agent calls %v", e.st.powerOns, ag.calls)
		}
	})
	t.Run("missing desktop is created", func(t *testing.T) {
		e := newEnv(t, nil)
		e.h.agent = ag
		rec := e.do("GET", "/apps/firefox", navHeaders)
		if rec.Code != 200 || len(e.st.creates) != 1 || len(ag.calls) != 0 {
			t.Fatalf("code %d creates %v calls %v", rec.Code, e.st.creates, ag.calls)
		}
	})
	t.Run("not navigation is 503 and starts nothing", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", lastStop(v1alpha1.StopIdle)))
		e.h.agent = ag
		rec := e.do("GET", "/apps/firefox", map[string]string{"Sec-Fetch-Mode": "cors"})
		if rec.Code != 503 || len(e.st.powerOns) != 0 || len(ag.calls) != 0 {
			t.Fatalf("code %d", rec.Code)
		}
	})
	t.Run("off page carries the app and start returns to it", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", lastStop(v1alpha1.StopIdle)))
		rec := e.do("GET", "/apps/org.kde.dolphin", navHeaders)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="app" value="org.kde.dolphin"`) {
			t.Fatalf("code %d body %s", rec.Code, rec.Body)
		}
		if rec := e.do("GET", "/", navHeaders); strings.Contains(rec.Body.String(), `name="app"`) {
			t.Error("plain off page must not carry an app")
		}
	})
	t.Run("start redirect", func(t *testing.T) {
		post := func(e *env, target, app string) *httptest.ResponseRecorder {
			form := url.Values{csrfField: {"tok"}}
			if app != "" {
				form.Set("app", app)
			}
			req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Remote-User", "alice")
			req.Header.Set("Remote-Groups", "admins")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.AddCookie(&http.Cookie{Name: csrfCookie, Value: "tok"})
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			return rec
		}
		admin := func(g *config.Gateway) { g.AdminGroup = "admins" }
		objs := func() []client.Object {
			return []client.Object{desktop("alice", "alice", lastStop(v1alpha1.StopIdle)), desktop("bob", "bob", lastStop(v1alpha1.StopIdle))}
		}
		for _, c := range []struct{ target, app, want string }{
			{"/.webtop/start", "firefox", "/apps/firefox"},
			{"/u/bob/.webtop/start", "firefox", "/u/bob/apps/firefox"},
			{"/.webtop/start", "", "/"},
			{"/.webtop/start", "//evil.example/x", "/"},
			{"/.webtop/start", "../x", "/"},
		} {
			e := newEnv(t, admin, objs()...)
			if rec := post(e, c.target, c.app); rec.Code != 303 || rec.Header().Get("Location") != c.want {
				t.Errorf("%+v: code %d loc %q", c, rec.Code, rec.Header().Get("Location"))
			}
		}
	})
}

func TestAgentClient(t *testing.T) {
	var gotPath, gotMethod string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(status)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	// The desktop's Status.URL names the web client port; the client swaps in the agent port.
	base, _ := url.Parse("http://" + u.Hostname() + ":8080")
	c := newAgentClient()
	c.port = port

	if err := c.Open(context.Background(), base, "org.kde.dolphin"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/api/v1/apps/org.kde.dolphin" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	status = http.StatusNotFound
	if err := c.Open(context.Background(), base, "x"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("404: %v", err)
	}
	for _, s := range []int{http.StatusBadGateway, http.StatusInternalServerError} {
		status = s
		if err := c.Open(context.Background(), base, "x"); err == nil || errors.Is(err, ErrAppNotFound) {
			t.Fatalf("%d: %v", s, err)
		}
	}
	srv.Close()
	if err := c.Open(context.Background(), base, "x"); err == nil {
		t.Fatal("closed agent: no error")
	}
}

func TestHostWithPort(t *testing.T) {
	for in, want := range map[string]string{
		"http://d.ns.svc:8080": "d.ns.svc:8081",
		"http://10.0.0.1:8080": "10.0.0.1:8081",
		"http://[fd00::1]:80":  "[fd00::1]:8081",
		"http://d.ns.svc":      "d.ns.svc:8081",
	} {
		u, _ := url.Parse(in)
		if got := hostWithPort(u, 8081); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
