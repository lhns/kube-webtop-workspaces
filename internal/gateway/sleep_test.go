// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

func asleep() dopt { return func(d *v1alpha1.Desktop) { d.Status.Asleep = true } }

func wakeCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == wakeCookie {
			return c
		}
	}
	return nil
}

func TestAsleepNavigationShowsSleepPage(t *testing.T) {
	b := htmlBackend(t, func(http.ResponseWriter, *http.Request) bool {
		t.Error("backend reached")
		return true
	})
	e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), asleep()))
	rec := e.do("GET", "/", navHeaders)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Your desktop is asleep") || !strings.Contains(body, `action="/.webtop/wake"`) ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code %d body %s", rec.Code, body)
	}
	var csrf *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookie {
			csrf = c
		}
	}
	if csrf == nil || !strings.Contains(body, `value="`+csrf.Value+`"`) {
		t.Fatalf("form lacks the csrf token: %v", rec.Result().Cookies())
	}
	if counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": outAsleep}) != 1 {
		t.Error("asleep outcome not counted")
	}

	// /apps/<name> keeps the app for the redirect after Wake; admins get their prefix.
	rec = e.do("GET", "/apps/org.kde.dolphin", navHeaders)
	if !strings.Contains(rec.Body.String(), `name="app" value="org.kde.dolphin"`) {
		t.Fatalf("app not carried: %s", rec.Body)
	}
	rec = e.do("GET", "/apps/", navHeaders)
	if !strings.Contains(rec.Body.String(), `name="app" value="*"`) {
		t.Fatalf("overview not carried: %s", rec.Body)
	}
	e = newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" }, desktop("bob", "bob", running(b.URL), asleep()))
	rec = e.do("GET", "/u/bob/", navHeaders, map[string]string{"Remote-Groups": "admins"})
	if !strings.Contains(rec.Body.String(), `action="/u/bob/.webtop/wake"`) {
		t.Fatalf("admin page: %s", rec.Body)
	}
}

func TestAsleepNonNavigationIsProxied(t *testing.T) {
	b := htmlBackend(t, nil)
	e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), asleep()))
	for _, hdr := range []map[string]string{
		{"Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"},
		{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"},
		nil,
	} {
		rec := e.do("GET", "/x", hdr)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "asleep") {
			t.Errorf("%v: code %d body %s", hdr, rec.Code, rec.Body)
		}
	}
	if rec := e.do("POST", "/x", navHeaders); rec.Code != 200 {
		t.Errorf("POST: code %d", rec.Code)
	}
}

func TestWakePost(t *testing.T) {
	const tok = "tok"
	post := func(e *env, target string, field string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
		if form == nil {
			form = url.Values{}
		}
		form.Set(csrfField, field)
		req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Remote-User", "alice")
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: tok})
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	b := htmlBackend(t, nil)

	t.Run("valid", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), asleep()))
		rec := post(e, "/.webtop/wake", tok, nil, same)
		c := wakeCookieOf(rec)
		if rec.Code != 303 || rec.Header().Get("Location") != "/" || c == nil {
			t.Fatalf("code %d loc %q cookie %v", rec.Code, rec.Header().Get("Location"), c)
		}
		if c.Path != "/" || c.MaxAge != wakeCookieMaxAge || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie = %+v", c)
		}
		if counter(t, e.reg, "webtop_gateway_requests_total", map[string]string{"outcome": outWake}) != 1 {
			t.Error("wake not counted")
		}

		// With the cookie the navigation reaches the desktop, and the cookie stays while it is asleep.
		rec = e.do("GET", "/", navHeaders, map[string]string{"Cookie": wakeCookie + "=1"})
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "asleep") || wakeCookieOf(rec) != nil {
			t.Fatalf("code %d body %s cookies %v", rec.Code, rec.Body, rec.Result().Cookies())
		}
	})
	t.Run("app and prefix", func(t *testing.T) {
		e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" }, desktop("bob", "bob", running(b.URL), asleep()))
		rec := post(e, "/u/bob/.webtop/wake", tok, url.Values{"app": {"firefox"}},
			map[string]string{"Sec-Fetch-Site": "same-origin", "Remote-Groups": "admins"})
		c := wakeCookieOf(rec)
		if over := post(e, "/u/bob/.webtop/wake", tok, url.Values{"app": {"*"}}, map[string]string{"Sec-Fetch-Site": "same-origin", "Remote-Groups": "admins"}); over.Header().Get("Location") != "/u/bob/apps/" {
			t.Errorf("overview loc %q", over.Header().Get("Location"))
		}
		if rec.Code != 303 || rec.Header().Get("Location") != "/u/bob/apps/firefox" || c == nil || c.Path != "/u/bob/" {
			t.Fatalf("code %d loc %q cookie %v", rec.Code, rec.Header().Get("Location"), c)
		}
	})
	t.Run("csrf failures", func(t *testing.T) {
		for name, tc := range map[string]struct {
			field string
			hdr   map[string]string
		}{
			"field mismatch": {"x", same},
			"cross-site":     {tok, map[string]string{"Sec-Fetch-Site": "cross-site"}},
			"no headers":     {tok, nil},
		} {
			e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), asleep()))
			rec := post(e, "/.webtop/wake", tc.field, nil, tc.hdr)
			if rec.Code != 403 || wakeCookieOf(rec) != nil {
				t.Errorf("%s: code %d cookie %v", name, rec.Code, wakeCookieOf(rec))
			}
		}
	})
	t.Run("GET not allowed", func(t *testing.T) {
		e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), asleep()))
		if rec := e.do("GET", "/.webtop/wake"); rec.Code != 405 {
			t.Fatalf("code %d", rec.Code)
		}
	})
	t.Run("someone else's desktop", func(t *testing.T) {
		e := newEnv(t, nil, desktop("bob", "bob", running(b.URL), asleep()))
		if rec := post(e, "/u/bob/.webtop/wake", tok, nil, same); rec.Code != 403 || wakeCookieOf(rec) != nil {
			t.Fatalf("code %d", rec.Code)
		}
	})
}

// Once the desktop is awake, a page load drops the wake cookie so the next Sleep shows the page.
func TestWakeCookieClearedWhenAwake(t *testing.T) {
	b := htmlBackend(t, nil)
	e := newEnv(t, nil, desktop("alice", "alice", running(b.URL)))
	rec := e.do("GET", "/", navHeaders, map[string]string{"Cookie": wakeCookie + "=1"})
	if c := wakeCookieOf(rec); rec.Code != 200 || c == nil || c.MaxAge >= 0 {
		t.Fatalf("code %d cookie %v", rec.Code, c)
	}
	if c := wakeCookieOf(e.do("GET", "/", navHeaders)); c != nil {
		t.Errorf("cookie set without one sent: %v", c)
	}
}

func TestSSEAsleep(t *testing.T) {
	b := htmlBackend(t, nil)
	e := newEnv(t, nil, desktop("alice", "alice", running(b.URL), asleep(), func(d *v1alpha1.Desktop) { d.Status.PodUID = "p" }))
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/.webtop/events?watch=1", nil)
	req.Header.Set("Remote-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ev, data, err := readEvent(t, bufio.NewReader(resp.Body))
	if err != nil || ev != "status" || !strings.Contains(data, `"asleep":true`) {
		t.Fatalf("event = %q %q %v", ev, data, err)
	}
}
