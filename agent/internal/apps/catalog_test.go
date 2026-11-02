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
	user, sys := t.TempDir(), t.TempDir()
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
	return &Launcher{Session: f, DataDirs: []string{user, sys}}, user, sys
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
		file, ct := resolveIcon(dirs, c.icon)
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
