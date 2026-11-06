// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

func seenAt(t time.Time) dopt {
	return func(d *v1alpha1.Desktop) { mt := metav1.NewTime(t); d.Spec.LastSeen = &mt }
}
func named(n string) dopt { return func(d *v1alpha1.Desktop) { d.Spec.DisplayName = n } }

func TestHeaderSetsDisplayName(t *testing.T) {
	e := newEnv(t, nil, desktop("alice", "alice"))
	if rec := e.do("GET", "/.webtop/ping", map[string]string{"Remote-Name": "Alice: <Smith>, \"Jr\""}); rec.Code != 204 {
		t.Fatal(rec.Code)
	}
	// Cleaned like the GECOS field; the markup stays, which is fine because it is never rendered.
	if d := e.desktop(t, "alice"); d.Spec.DisplayName != "Alice <Smith> Jr" || d.Spec.LastSeen == nil {
		t.Fatalf("spec %+v", d.Spec)
	}
}

func TestMissingHeaderKeepsName(t *testing.T) {
	e := newEnv(t, nil, desktop("alice", "alice", named("Old Name")))
	e.do("GET", "/.webtop/ping")
	if d := e.desktop(t, "alice"); d.Spec.DisplayName != "Old Name" || d.Spec.LastSeen == nil {
		t.Fatalf("spec %+v", d.Spec)
	}
}

func TestLastSeenThrottle(t *testing.T) {
	now := time.Now()
	e := newEnv(t, nil, desktop("alice", "alice", seenAt(now.Add(-10*time.Minute))))
	e.h.now = func() time.Time { return now }
	e.do("GET", "/.webtop/ping")
	if d := e.desktop(t, "alice"); !d.Spec.LastSeen.Time.Before(now.Add(-time.Minute)) {
		t.Fatal("a fresh lastSeen was rewritten")
	}
	// Stale: written once, and not again within the retry spacing.
	e.h.now = func() time.Time { return now.Add(2 * time.Hour) }
	e.do("GET", "/.webtop/ping")
	first := e.desktop(t, "alice").Spec.LastSeen.Time
	if first.Before(now.Add(time.Hour)) {
		t.Fatal("a stale lastSeen was not written")
	}
	e.h.now = func() time.Time { return now.Add(2*time.Hour + time.Second) }
	e.do("GET", "/.webtop/ping")
	if got := e.desktop(t, "alice").Spec.LastSeen.Time; !got.Equal(first) {
		t.Fatal("written twice in a row")
	}
}

func TestRefusedRequestDoesNotUpdateLastSeen(t *testing.T) {
	e := newEnv(t, func(g *config.Gateway) { g.RequiredGroup = "desktops" }, desktop("alice", "alice"))
	if rec := e.do("GET", "/.webtop/ping", map[string]string{"Remote-Groups": "other", "Remote-Name": "Mallory"}); rec.Code != 403 {
		t.Fatalf("code %d", rec.Code)
	}
	if d := e.desktop(t, "alice"); d.Spec.LastSeen != nil || d.Spec.DisplayName != "" {
		t.Fatalf("spec %+v", d.Spec)
	}
	if rec := e.do("GET", "/.webtop/ping", map[string]string{"Remote-Groups": "desktops"}); rec.Code != 204 {
		t.Fatalf("code %d", rec.Code)
	}
	if e.desktop(t, "alice").Spec.LastSeen == nil {
		t.Fatal("allowed request not recorded")
	}
}

func TestAdminDoesNotTouchSomeoneElse(t *testing.T) {
	e := newEnv(t, func(g *config.Gateway) { g.AdminGroup = "admins" }, desktop("bob", "bob", named("Bob")))
	e.do("GET", "/u/bob/.webtop/ping", map[string]string{"Remote-Groups": "admins", "Remote-Name": "Alice"})
	if d := e.desktop(t, "bob"); d.Spec.LastSeen != nil || d.Spec.DisplayName != "Bob" {
		t.Fatalf("spec %+v", d.Spec)
	}
}

func TestCreateCarriesNameAndLastSeen(t *testing.T) {
	e := newEnv(t, nil)
	e.do("GET", "/", navHeaders, map[string]string{"Remote-Name": "Alice A"})
	if d := e.desktop(t, "alice"); d.Spec.DisplayName != "Alice A" || d.Spec.LastSeen == nil {
		t.Fatalf("spec %+v", d.Spec)
	}
}

func TestLogoutRedirect(t *testing.T) {
	const target = "https://auth.example.org/logout?rd=https%3A%2F%2Fapp.example.org"
	withURL := func(g *config.Gateway) { g.LogoutURL = target; g.AdminGroup = "admins" }
	e := newEnv(t, withURL, desktop("alice", "alice"), desktop("bob", "bob"))

	rec := e.do("GET", "/.webtop/logout")
	if rec.Code != 303 || rec.Header().Get("Location") != target {
		t.Fatalf("owner: %d %v", rec.Code, rec.Header())
	}
	if rec := e.do("GET", "/.webtop/logout", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 403 {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if rec := e.do("GET", "/.webtop/logout", map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != 303 {
		t.Fatalf("same-origin: %d", rec.Code)
	}
	// An admin looking at bob's desktop is not bob.
	if rec := e.do("GET", "/u/bob/.webtop/logout", map[string]string{"Remote-Groups": "admins"}); rec.Code != 403 {
		t.Fatalf("admin on another desktop: %d", rec.Code)
	}
	if rec := e.do("POST", "/.webtop/logout"); rec.Code != 405 {
		t.Fatalf("POST: %d", rec.Code)
	}

	none := newEnv(t, nil, desktop("alice", "alice"))
	if rec := none.do("GET", "/.webtop/logout"); rec.Code != 404 || rec.Header().Get("Location") != "" {
		t.Fatalf("no URL configured: %d", rec.Code)
	}
}

func TestLogoutEventOnlyForOwnerWithURL(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	d := desktop("alice", "alice", func(d *v1alpha1.Desktop) { d.Status.LastLogout = &at })
	if ev := eventOf(d, time.Now(), true); ev.LoggedOut != "2026-01-02T03:04:05Z" {
		t.Fatalf("owner: %+v", ev)
	}
	if ev := eventOf(d, time.Now(), false); ev.LoggedOut != "" {
		t.Fatalf("not owner or no URL: %+v", ev)
	}
}
