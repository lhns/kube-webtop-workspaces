package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, elem ...string) string {
	t.Helper()
	body := elem[len(elem)-1]
	p := filepath.Join(elem[:len(elem)-1]...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func entry(name, extra string) string {
	return "[Desktop Entry]\nType=Application\nName=" + name + "\nName[de]=Lokal\n" + extra + "\n[Desktop Action x]\nName=Other\nHidden=true\n"
}

func catalog(t *testing.T) (*Launcher, string, string) {
	t.Helper()
	home, sys := t.TempDir(), t.TempDir() // sys stands in for the Nix store
	user := filepath.Join(home, ".local", "share")
	apps := func(d, f, body string) { write(t, d, "applications", f, body) }
	apps(sys, "org.kde.dolphin.desktop", entry("Dolphin", "Icon=system-file-manager"))
	apps(sys, "firefox.desktop", entry("firefox", "Icon=firefox"))
	apps(sys, "kate.desktop", entry("Kate", "OnlyShowIn=KDE;"))
	apps(sys, "ok-ukui.desktop", entry("Ukui", "OnlyShowIn=UKUI;"))
	apps(sys, "gnome.desktop", entry("Gnome", "NotShowIn=GNOME;KDE;"))
	apps(sys, "hidden.desktop", entry("Hidden", "NoDisplay=true"))
	apps(sys, "gone.desktop", entry("Gone", "Hidden=true"))
	apps(sys, "link.desktop", "[Desktop Entry]\nType=Link\nName=Link\n")
	apps(sys, "bad name.desktop", entry("Bad", ""))
	apps(sys, "noicon.desktop", entry("Zed", ""))
	apps(sys, "shadowed.desktop", entry("Hidden by user", ""))
	apps(user, "shadowed.desktop", entry("Hidden by user", "Hidden=true"))
	apps(user, "firefox.desktop", entry("Firefox (user)", "Icon=firefox"))
	write(t, sys, "icons", "hicolor", "48x48", "apps", "firefox.png", "png")
	write(t, sys, "icons", "breeze", "scalable", "apps", "system-file-manager.svg", "<svg/>")
	f := &fakeSession{units: "app-org.kde.dolphin@abc.service loaded active\napp-kate-1234.scope loaded active\n"}
	return &Launcher{Session: f, DataDirs: []string{user, sys}, Home: home, StoreRoot: sys}, user, sys
}

func TestListFiltersSortsAndPrefersEarlierDirs(t *testing.T) {
	l, _, _ := catalog(t)
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []App{
		{ID: "org.kde.dolphin", Name: "Dolphin", Icon: true, Running: true},
		{ID: "firefox", Name: "Firefox (user)", Icon: true},
		{ID: "kate", Name: "Kate", Running: true},
		{ID: "noicon", Name: "Zed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestListEmptyAndUnitsFailure(t *testing.T) {
	l := &Launcher{Session: &fakeSession{unitsFail: true}, DataDirs: []string{t.TempDir()}}
	got, err := l.List(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v %v, want an empty non-nil list", got, err)
	}
	l, _, _ = catalog(t)
	l.Session = &fakeSession{unitsFail: true}
	if got, err := l.List(context.Background()); err != nil || len(got) != 4 || got[0].Running {
		t.Fatalf("units failure must still list: %+v %v", got, err)
	}
}

func TestResolveIcon(t *testing.T) {
	d := t.TempDir()
	outside := t.TempDir()
	write(t, d, "icons", "hicolor", "48x48", "apps", "small.png", "x")
	write(t, d, "icons", "hicolor", "256x256", "apps", "small.png", "x")
	write(t, d, "icons", "breeze", "scalable", "apps", "both.svg", "x")
	write(t, d, "icons", "breeze", "scalable", "apps", "both.png", "x")
	write(t, d, "pixmaps", "pix.png", "x")
	write(t, d, "pixmaps", "old.xpm", "x")
	write(t, d, "pixmaps", "big.png", strings.Repeat("x", maxIcon+1))
	write(t, d, "pixmaps", "empty.png", "")
	write(t, d, "app", "abs.svg", "x")
	write(t, d, "app", "abs.xpm", "x")
	write(t, outside, "secret.png", "x")
	dirs := []string{d}
	g := (&Launcher{StoreRoot: d}).newGuard()
	for _, c := range []struct{ icon, file, ct string }{
		{"small", "icons/hicolor/256x256/apps/small.png", "image/png"},
		{"both", "icons/breeze/scalable/apps/both.svg", "image/svg+xml"},
		{"pix", "pixmaps/pix.png", "image/png"},
		{"old", "", ""},
		{"big", "", ""},
		{"empty", "", ""},
		{"missing", "", ""},
		{"", "", ""},
		{"../pixmaps/pix", "", ""},
		{filepath.Join(d, "app", "abs.svg"), "app/abs.svg", "image/svg+xml"},
		{filepath.Join(d, "app", "abs.xpm"), "", ""},
		{filepath.Join(d, "app", "..", "..", "x", "secret.png"), "", ""},
		{filepath.Join(outside, "secret.png"), "", ""},
	} {
		file, ct := resolveIcon(g, dirs, c.icon)
		want := ""
		if c.file != "" {
			want = filepath.Join(d, filepath.FromSlash(c.file))
		}
		if file != want || ct != c.ct {
			t.Errorf("%q: got %q %q, want %q %q", c.icon, file, ct, want, c.ct)
		}
	}
}

func TestListAndIconHandlers(t *testing.T) {
	l, _, sys := catalog(t)
	h := Handler(l)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	rec := get("/api/v1/apps")
	var list []App
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 4 {
		t.Fatalf("list: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}

	rec = get("/api/v1/apps/org.kde.dolphin/icon")
	if rec.Code != 200 || rec.Body.String() != "<svg/>" || rec.Header().Get("Content-Type") != "image/svg+xml" ||
		rec.Header().Get("Cache-Control") != "max-age=3600" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("icon: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if rec := get("/api/v1/apps/firefox/icon"); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("png icon: %d %v", rec.Code, rec.Header())
	}
	for _, name := range []string{"noicon", "nope", "gnome"} {
		if rec := get("/api/v1/apps/" + name + "/icon"); rec.Code != 404 {
			t.Errorf("%s: %d, want 404", name, rec.Code)
		}
	}
	// A hidden entry's icon is still resolvable, the listing is what hides it.
	write(t, sys, "icons", "hicolor", "48x48", "apps", "hidden.png", "x")
	write(t, sys, "applications", "hidden.desktop", entry("Hidden", "NoDisplay=true\nIcon=hidden"))
	if rec := get("/api/v1/apps/hidden/icon"); rec.Code != 200 {
		t.Errorf("hidden entry icon: %d", rec.Code)
	}
}

func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
}

// The agent runs as root and ~/.local/share belongs to the user: links out of it must not be followed.
func TestSymlinksOutOfTheUserTreeAreRefused(t *testing.T) {
	l, user, _ := catalog(t)
	secret := write(t, t.TempDir(), "secret.png", "root only")
	secretDesktop := write(t, t.TempDir(), "secret.desktop", entry("Leaked", "Icon=leak"))
	link(t, secret, filepath.Join(user, "icons", "hicolor", "48x48", "apps", "leak.png"))
	link(t, secret, filepath.Join(user, "pixmaps", "abs.png"))
	link(t, secretDesktop, filepath.Join(user, "applications", "leak.desktop"))
	write(t, user, "applications", "viaabs.desktop", entry("Via abs", "Icon="+filepath.Join(user, "pixmaps", "abs.png")))
	write(t, user, "applications", "viaicon.desktop", entry("Via icon", "Icon=leak"))
	// A user dir that is itself a link to somewhere else is refused as a whole.
	elsewhere := t.TempDir()
	write(t, elsewhere, "applications", "evil.desktop", entry("Evil", ""))

	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a.ID == "leak" || a.Name == "Leaked" {
			t.Errorf("listed a desktop file reached through a link out of the user tree: %+v", a)
		}
		if (a.ID == "viaabs" || a.ID == "viaicon") && a.Icon {
			t.Errorf("%s: icon resolved through a link out of the user tree", a.ID)
		}
	}
	for _, id := range []string{"viaabs", "viaicon"} {
		if p, _, err := l.Icon(context.Background(), id); err == nil {
			t.Errorf("%s: served %s", id, p)
		}
	}

	// Links inside the user tree are fine.
	write(t, user, "pixmaps", "real.png", "x")
	link(t, filepath.Join(user, "pixmaps", "real.png"), filepath.Join(user, "pixmaps", "alias.png"))
	write(t, user, "applications", "alias.desktop", entry("Alias", "Icon=alias"))
	if p, _, err := l.Icon(context.Background(), "alias"); err != nil || filepath.Base(p) != "real.png" {
		t.Errorf("link inside the user tree: %q %v", p, err)
	}

	// The whole user data home redirected out of the home is not trusted.
	home2 := t.TempDir()
	link(t, elsewhere, filepath.Join(home2, ".local", "share"))
	l2 := &Launcher{Session: &fakeSession{}, DataDirs: []string{filepath.Join(home2, ".local", "share")}, Home: home2, StoreRoot: t.TempDir()}
	if got, _ := l2.List(context.Background()); len(got) != 0 {
		t.Errorf("redirected data home was read: %+v", got)
	}
}

// A Nix profile is a farm of links into the store: those are accepted.
func TestStoreSymlinkFarmIsAccepted(t *testing.T) {
	store, profile, home := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, store, "pkg", "share", "applications", "farm.desktop", entry("Farm", "Icon=farm"))
	write(t, store, "pkg", "share", "icons", "hicolor", "48x48", "apps", "farm.png", "x")
	link(t, filepath.Join(store, "pkg", "share", "applications", "farm.desktop"), filepath.Join(profile, "applications", "farm.desktop"))
	link(t, filepath.Join(store, "pkg", "share", "icons"), filepath.Join(profile, "icons"))
	l := &Launcher{Session: &fakeSession{}, DataDirs: []string{profile}, Home: home, StoreRoot: store}
	got, err := l.List(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "farm" || !got[0].Icon {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, ct, err := l.Icon(context.Background(), "farm"); err != nil || ct != "image/png" {
		t.Errorf("icon: %q %v", ct, err)
	}
	// The same farm pointing outside the store is refused.
	l.StoreRoot = t.TempDir()
	if got, _ := l.List(context.Background()); len(got) != 0 {
		t.Errorf("farm outside the store listed: %+v", got)
	}
}
