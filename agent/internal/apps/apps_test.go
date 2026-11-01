package apps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeSession records the user-session commands in order and answers from canned values.
type fakeSession struct {
	calls     []string
	env       string
	units     string
	failOn    string // a command line prefix that fails
	unitsFail bool
}

func (f *fakeSession) Systemctl(_ context.Context, args ...string) ([]byte, error) {
	line := "systemctl " + strings.Join(args, " ")
	switch args[0] {
	case "show-environment":
		return []byte(f.env), nil
	case "list-units":
		if f.unitsFail {
			return nil, errors.New("boom")
		}
		return []byte(f.units), nil
	}
	f.calls = append(f.calls, line)
	return nil, nil
}

func (f *fakeSession) Command(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if f.failOn != "" && strings.HasPrefix(line, f.failOn) {
		return nil, errors.New("exit status 1")
	}
	return nil, nil
}

const dolphin = "/usr/share/applications/org.kde.dolphin.desktop"

func newLauncher(t *testing.T, f *fakeSession) *Launcher {
	t.Helper()
	f.env = "XDG_DATA_DIRS=/usr/share\nWAYLAND_DISPLAY=wayland-0\n"
	return &Launcher{
		Session: f, StateDir: t.TempDir(), Home: "/home/user",
		Exists: func(p string) bool { return p == dolphin || p == "/usr/share/applications/firefox.desktop" },
	}
}

func TestOpenPlacesThenLaunches(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	if err := l.Open(context.Background(), "org.kde.dolphin"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"webtop-app-screen place org.kde.dolphin SELKIES-2",
		"systemd-run --user --collect --quiet --description=webtop app org.kde.dolphin -- kioclient exec " + dolphin,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if got, _ := os.ReadFile(filepath.Join(l.StateDir, "placed-app")); string(got) != "org.kde.dolphin\n" {
		t.Fatalf("state %q", got)
	}
}

func TestOpenReleasesPreviousFirst(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	if err := os.WriteFile(filepath.Join(l.StateDir, "placed-app"), []byte("firefox\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.Open(context.Background(), "org.kde.dolphin"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) < 3 || f.calls[0] != "webtop-app-screen release firefox" ||
		f.calls[1] != "webtop-app-screen place org.kde.dolphin SELKIES-2" || !strings.HasPrefix(f.calls[2], "systemd-run") {
		t.Fatalf("order: %v", f.calls)
	}
}

func TestOpenIdempotentWhenRunning(t *testing.T) {
	f := &fakeSession{units: "app-org.kde.dolphin@0123abcd.service loaded active running Dolphin\n"}
	l := newLauncher(t, f)
	for i := 0; i < 2; i++ {
		if err := l.Open(context.Background(), "org.kde.dolphin"); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "systemd-run") {
			t.Fatalf("launched although running: %v", f.calls)
		}
	}
	if len(f.calls) != 2 || f.calls[0] != f.calls[1] { // place twice, never release itself
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestOpenLaunchesWhenUnitListFails(t *testing.T) {
	f := &fakeSession{unitsFail: true}
	l := newLauncher(t, f)
	if err := l.Open(context.Background(), "firefox"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.calls[len(f.calls)-1], "systemd-run") {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestOpenErrors(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	ctx := context.Background()
	for _, bad := range []string{"", "a b", "a/b", "../x", "x;y"} {
		if err := l.Open(ctx, bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := l.Open(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown app: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("an invalid or unknown app must not touch the session: %v", f.calls)
	}

	f = &fakeSession{failOn: "webtop-app-screen place"}
	l = newLauncher(t, f)
	if err := l.Open(ctx, "firefox"); !errors.Is(err, ErrPlace) {
		t.Errorf("place failure: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("must not launch after a failed place: %v", f.calls)
	}

	f = &fakeSession{failOn: "systemd-run"}
	l = newLauncher(t, f)
	if err := l.Open(ctx, "firefox"); !errors.Is(err, ErrLaunch) {
		t.Errorf("launch failure: %v", err)
	}
}

func TestUnitIsApp(t *testing.T) {
	for unit, want := range map[string]bool{
		"app-firefox@abc.service":                        true,
		"app-firefox-1234.scope":                         true,
		"app-firefox.service":                            true,
		"app-org.kde.plasma\\x2dsystemmonitor@1.service": true,
		"app-firefox-esr-1234.scope":                     false,
		"app-firefoxy@1.service":                         false,
		"firefox.service":                                false,
		"app-org.kde.dolphin@1.service":                  false,
	} {
		name := "firefox"
		if strings.Contains(unit, "systemmonitor") {
			name = "org.kde.plasma-systemmonitor"
		}
		if got := unitIsApp(unit, name); got != want {
			t.Errorf("%s: got %v", unit, got)
		}
	}
}

type fakeOpener struct{ err error }

func (f fakeOpener) Open(context.Context, string) error { return f.err }

func TestHandlerMapsErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{nil, 204},
		{ErrInvalidName, 400},
		{ErrNotFound, 404},
		{ErrPlace, 502},
		{ErrLaunch, 500},
		{errors.New("x"), 500},
	} {
		rec := httptest.NewRecorder()
		Handler(fakeOpener{c.err}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/apps/x", nil))
		if rec.Code != c.code {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.code)
		}
	}
	rec := httptest.NewRecorder()
	Handler(fakeOpener{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apps/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
}
