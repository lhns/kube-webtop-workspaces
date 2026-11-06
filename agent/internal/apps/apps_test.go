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
	"time"
)

// fakeSession records the user-session commands in order and answers from canned values.
type fakeSession struct {
	calls       []string
	env         string
	units       string
	failOn      string // a command line prefix that fails
	unitsFail   bool
	screens     string // output of `webtop-app-screen screens`
	screensFail bool
}

func (f *fakeSession) Systemctl(_ context.Context, args ...string) ([]byte, error) {
	line := "systemctl " + strings.Join(args, " ")
	switch args[0] {
	case "show-environment":
		return []byte(f.env), nil
	}
	f.calls = append(f.calls, line)
	return nil, nil
}

// ActiveUnits answers with the first field of each line of units (list-units output).
func (f *fakeSession) ActiveUnits(context.Context) ([]string, error) {
	if f.unitsFail {
		return nil, errors.New("boom")
	}
	var units []string
	for _, l := range strings.Split(f.units, "\n") {
		if fs := strings.Fields(l); len(fs) > 0 {
			units = append(units, fs[0])
		}
	}
	return units, nil
}

func (f *fakeSession) Command(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	if line == "webtop-app-screen screens" { // not recorded: Open asks every time
		if f.screensFail {
			return nil, errors.New("exit status 1")
		}
		return []byte(f.screens), nil
	}
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

func open(t *testing.T, l *Launcher, name string) int {
	t.Helper()
	n, err := l.Open(context.Background(), name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	return n
}

func TestOpenPlacesThenLaunches(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	if n := open(t, l, "org.kde.dolphin"); n != 3 {
		t.Fatalf("display %d, want 3", n)
	}
	want := []string{
		"webtop-app-screen place org.kde.dolphin SELKIES-3",
		"systemd-run --user --collect --quiet --description=webtop app org.kde.dolphin -- kioclient exec " + dolphin,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if got, _ := os.ReadFile(filepath.Join(l.StateDir, "app-screens")); string(got) != `{"next":4,"apps":{"org.kde.dolphin":3}}` {
		t.Fatalf("state %q", got)
	}
}

func TestOpenDistinctStickyNumbers(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	a, b := open(t, l, "org.kde.dolphin"), open(t, l, "firefox")
	if a != 3 || b != 4 {
		t.Fatalf("got %d, %d", a, b)
	}
	if open(t, l, "org.kde.dolphin") != 3 {
		t.Fatal("same app must keep its number")
	}
	// A restarted agent reads the file: numbers stay.
	l2 := &Launcher{Session: f, StateDir: l.StateDir, Exists: l.Exists}
	if open(t, l2, "firefox") != 4 {
		t.Fatal("number lost over a restart")
	}
}

func TestOpenNeverReusesNumbers(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	l.MaxScreens = 1
	now := time.Now()
	l.Now = func() time.Time { return now }
	if open(t, l, "org.kde.dolphin") != 3 {
		t.Fatal("first")
	}
	now = now.Add(2 * reserveTTL) // its page never connected: the reservation lapses, the screen is free
	if n := open(t, l, "firefox"); n != 4 {
		t.Fatalf("a freed number was reused: %d", n)
	}
}

func TestOpenCap(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	l.MaxScreens = 2
	now := time.Now()
	l.Now = func() time.Time { return now }
	open(t, l, "org.kde.dolphin")
	open(t, l, "firefox")
	f.calls = nil
	l.Exists = func(p string) bool { return true }
	_, err := l.Open(context.Background(), "kate")
	var ce *CapError
	if !errors.As(err, &ce) || ce.Max != 2 || !reflect.DeepEqual(ce.Open, []string{"firefox", "org.kde.dolphin"}) {
		t.Fatalf("err %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("the rejected app must not be placed or started: %v", f.calls)
	}
	// Re-attach is never rejected.
	if open(t, l, "firefox") != 4 {
		t.Fatal("re-attach")
	}
	// Reservations lapse; a live screen counts instead.
	now = now.Add(2 * reserveTTL)
	f.screens = "SELKIES-3\nSELKIES-4\n"
	if _, err := l.Open(context.Background(), "kate"); !errors.As(err, &ce) {
		t.Fatalf("live screens must count: %v", err)
	}
	if open(t, l, "org.kde.dolphin") != 3 {
		t.Fatal("an app with a live screen re-attaches")
	}
	f.screens = "SELKIES-3\n"
	if n := open(t, l, "kate"); n != 5 {
		t.Fatalf("kate %d", n)
	}
}

func TestOpenScreensFailure(t *testing.T) {
	f := &fakeSession{screensFail: true}
	l := newLauncher(t, f)
	if _, err := l.Open(context.Background(), "firefox"); !errors.Is(err, ErrPlace) {
		t.Fatalf("err %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestFocus(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	ctx := context.Background()
	open(t, l, "firefox")
	f.calls = nil
	if err := l.Focus(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := l.Focus(ctx, "3"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"webtop-app-screen focus main", "webtop-app-screen focus SELKIES-3"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
	if err := l.Focus(ctx, "9"); !errors.Is(err, ErrUnknownScreen) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestOpenIdempotentWhenRunning(t *testing.T) {
	f := &fakeSession{units: "app-org.kde.dolphin@0123abcd.service loaded active running Dolphin\n"}
	l := newLauncher(t, f)
	for i := 0; i < 2; i++ {
		open(t, l, "org.kde.dolphin")
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
	open(t, l, "firefox")
	if !strings.HasPrefix(f.calls[len(f.calls)-1], "systemd-run") {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestOpenErrors(t *testing.T) {
	f := &fakeSession{}
	l := newLauncher(t, f)
	ctx := context.Background()
	for _, bad := range []string{"", "a b", "a/b", "../x", "x;y"} {
		if _, err := l.Open(ctx, bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := l.Open(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown app: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("an invalid or unknown app must not touch the session: %v", f.calls)
	}

	f = &fakeSession{failOn: "webtop-app-screen place"}
	l = newLauncher(t, f)
	if _, err := l.Open(ctx, "firefox"); !errors.Is(err, ErrPlace) {
		t.Errorf("place failure: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("must not launch after a failed place: %v", f.calls)
	}

	f = &fakeSession{failOn: "systemd-run"}
	l = newLauncher(t, f)
	if _, err := l.Open(ctx, "firefox"); !errors.Is(err, ErrLaunch) {
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

func (f fakeOpener) Open(context.Context, string) (int, error) { return 5, f.err }

func (f fakeOpener) Focus(_ context.Context, screen string) error {
	if screen == "7" {
		return ErrUnknownScreen
	}
	return f.err
}

func (f fakeOpener) List(context.Context) ([]App, error) { return nil, nil }

func (f fakeOpener) Icon(context.Context, string) (string, string, error) { return "", "", ErrNotFound }

func TestHandlerMapsErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
	}{
		{nil, 200},
		{&CapError{Max: 2, Open: []string{"a", "b"}}, 409},
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
	Handler(fakeOpener{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/apps/x", nil))
	if rec.Body.String() != "{\"display\":5}\n" {
		t.Errorf("body %q", rec.Body)
	}
	rec = httptest.NewRecorder()
	Handler(fakeOpener{&CapError{Max: 2, Open: []string{"a", "b"}}}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/apps/x", nil))
	if rec.Body.String() != "{\"max\":2,\"open\":[\"a\",\"b\"]}\n" {
		t.Errorf("409 body %q", rec.Body)
	}
	rec = httptest.NewRecorder()
	Handler(fakeOpener{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/apps/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
}

func TestHandlerFocus(t *testing.T) {
	for path, want := range map[string]int{
		"main": 204, "3": 204, "7": 404, "x": 400, "-1": 400, "1234567890": 400, "3a": 400, "": 404,
	} {
		rec := httptest.NewRecorder()
		Handler(fakeOpener{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/focus/"+path, nil))
		if rec.Code != want {
			t.Errorf("focus %q: %d, want %d", path, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	Handler(fakeOpener{ErrPlace}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/focus/main", nil))
	if rec.Code != 502 {
		t.Errorf("failure: %d", rec.Code)
	}
}
