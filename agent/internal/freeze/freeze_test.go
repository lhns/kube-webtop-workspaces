package freeze

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

const um = "/user.slice/user-1000.slice/user@1000.service/"

func u(name, slice string) Unit { return Unit{name, um + slice + "/" + name} }

func TestSelect(t *testing.T) {
	got := Select([]Unit{
		u("plasma-kwin_wayland.service", "session.slice"),
		u("plasma-plasmashell.service", "session.slice"),
		u("pipewire.service", "session.slice"),
		u("wireplumber.service", "session.slice"),
		u("dbus-broker.service", "session.slice"),
		u("at-spi-dbus-bus.service", "session.slice"),
		u("selkies.service", "app.slice"),
		u("app-firefox-123.scope", "app.slice/app-firefox.slice"),
		u("xdg-desktop-portal.service", "app.slice"),
		u("tracker.service", "background.slice"),
		{"user@1000.service", "/user.slice/user-1000.slice/user@1000.service"},
		{"webtop-agent.service", "/system.slice/webtop-agent.service"},
		u("plasma-core.target", "session.slice"),
		u("pipewire-pulse.service", "app.slice"), // misplaced, still protected
	})
	want := []string{"app-firefox-123.scope", "plasma-kwin_wayland.service", "plasma-plasmashell.service", "xdg-desktop-portal.service"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestWakeRule(t *testing.T) {
	tests := []struct {
		name     string
		baseline []string
		seq      [][]string
		want     []bool
	}{
		{"nobody connected: first client wakes", nil, [][]string{{}, {"a"}}, []bool{false, true}},
		{"open tab alone does not wake", []string{"a"}, [][]string{{"a"}, {"a"}}, []bool{false, false}},
		{"tab closed then reopened", []string{"a"}, [][]string{{"a"}, {}, {"b"}}, []bool{false, false, true}},
		{"reload under a new id", []string{"a"}, [][]string{{"b"}}, []bool{true}},
		{"reload seen as 0 then 1, no ids", []string{""}, [][]string{{}, {""}}, []bool{false, true}},
		{"reload between polls, no ids: not detectable", []string{""}, [][]string{{""}}, []bool{false}},
		{"second tab wakes", []string{"a"}, [][]string{{"a", "b"}}, []bool{true}},
		{"one of two tabs closes", []string{"a", "b"}, [][]string{{"a"}}, []bool{false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewWakeRule(tc.baseline)
			for i, ids := range tc.seq {
				if got := w.Observe(ids); got != tc.want[i] {
					t.Fatalf("step %d: got %v, want %v", i, got, tc.want[i])
				}
			}
		})
	}
}

type fakeCtl struct {
	mu        sync.Mutex
	units     []Unit
	frozen    map[string]bool
	events    []string
	freezeErr map[string]error // a Freeze call including the unit fails
	gone      map[string]bool  // a failing unit also disappears from the listing
	thawErr   error
}

func newCtl(units ...Unit) *fakeCtl {
	return &fakeCtl{units: units, frozen: map[string]bool{}, freezeErr: map[string]error{}, gone: map[string]bool{}}
}

func (c *fakeCtl) Units(context.Context) ([]Unit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.units), nil
}

func (c *fakeCtl) Freeze(_ context.Context, units ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range units {
		if err := c.freezeErr[n]; err != nil {
			if c.gone[n] {
				c.units = slices.DeleteFunc(c.units, func(x Unit) bool { return x.Name == n })
			}
			return err
		}
	}
	for _, n := range units {
		c.frozen[n] = true
	}
	c.events = append(c.events, "freeze")
	return nil
}

func (c *fakeCtl) Thaw(_ context.Context, units ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.thawErr != nil {
		return c.thawErr
	}
	for _, n := range units {
		delete(c.frozen, n)
	}
	c.events = append(c.events, "thaw")
	return nil
}

func (c *fakeCtl) nFrozen() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.frozen) }

func session() []Unit {
	return []Unit{u("plasma-kwin_wayland.service", "session.slice"), u("app-kate.scope", "app.slice"), u("selkies.service", "app.slice")}
}

func noSessions(context.Context) ([]string, error) { return nil, nil }

func TestFreezeFailureThawsAndFallsBack(t *testing.T) {
	ctl := newCtl(session()...)
	ctl.freezeErr["app-kate.scope"] = errors.New("freezer unavailable")
	dir := t.TempDir()
	var notified []bool
	s := &Sleeper{Freezer: &Freezer{Ctl: ctl, StateDir: dir}, Poll: time.Millisecond,
		Notify: func(b bool) { notified = append(notified, b) }, Sessions: noSessions}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("want an error so the caller powers off")
	}
	if ctl.nFrozen() != 0 || Asleep(dir) || len(notified) != 0 {
		t.Fatalf("left frozen=%d asleep=%v notified=%v", ctl.nFrozen(), Asleep(dir), notified)
	}
}

func TestUnitExitingDuringFreezeIsSkipped(t *testing.T) {
	ctl := newCtl(session()...)
	ctl.freezeErr["app-kate.scope"] = errors.New("not loaded")
	ctl.gone["app-kate.scope"] = true
	dir := t.TempDir()
	units, err := (&Freezer{Ctl: ctl, StateDir: dir}).Freeze(context.Background())
	if err != nil || !reflect.DeepEqual(units, []string{"plasma-kwin_wayland.service"}) {
		t.Fatalf("got %v, %v", units, err)
	}
	if got := record(t, dir); got != "plasma-kwin_wayland.service" {
		t.Fatalf("record %q", got)
	}
}

func record(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "frozen-units"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b[:len(b)-1])
}

func TestSessionsErrorBeforeFreeze(t *testing.T) {
	ctl := newCtl(session()...)
	s := &Sleeper{Freezer: &Freezer{Ctl: ctl, StateDir: t.TempDir()}, Poll: time.Millisecond, Notify: func(bool) {},
		Sessions: func(context.Context) ([]string, error) { return nil, errors.New("selkies down") }}
	if err := s.Run(context.Background()); err == nil || ctl.nFrozen() != 0 {
		t.Fatalf("err=%v frozen=%d", err, ctl.nFrozen())
	}
}

func TestSleepWakesOnNewClient(t *testing.T) {
	ctl := newCtl(session()...)
	dir := t.TempDir()
	var mu sync.Mutex
	calls := 0
	s := &Sleeper{Freezer: &Freezer{Ctl: ctl, StateDir: dir}, Poll: time.Millisecond, Notify: func(bool) {},
		Sessions: func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls <= 3 { // the baseline, then the tab that clicked Sleep
				return []string{"a"}, nil
			}
			return []string{"b"}, nil // it reloaded
		}}
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ctl.nFrozen() != 0 || Asleep(dir) || ctl.events[len(ctl.events)-1] != "thaw" || calls < 4 {
		t.Fatalf("frozen=%d events=%v calls=%d", ctl.nFrozen(), ctl.events, calls)
	}
}

func TestSigtermThawsBeforeReturning(t *testing.T) {
	ctl := newCtl(session()...)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var notified []bool
	s := &Sleeper{Freezer: &Freezer{Ctl: ctl, StateDir: dir}, Poll: time.Millisecond,
		Notify:   func(b bool) { mu.Lock(); notified = append(notified, b); mu.Unlock() },
		Sessions: func(context.Context) ([]string, error) { return []string{"a"}, nil }}
	done := make(chan error)
	go func() { done <- s.Run(ctx) }()
	for ctl.nFrozen() == 0 {
		time.Sleep(time.Millisecond)
	}
	if !Asleep(dir) {
		t.Fatal("not recorded as asleep")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ctl.nFrozen() != 0 || Asleep(dir) || !reflect.DeepEqual(notified, []bool{true, false}) {
		t.Fatalf("frozen=%d asleep=%v notified=%v", ctl.nFrozen(), Asleep(dir), notified)
	}
}

func TestThawFromAnotherProcess(t *testing.T) {
	ctl := newCtl(session()...)
	dir := t.TempDir()
	if _, err := (&Freezer{Ctl: ctl, StateDir: dir}).Freeze(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A fresh Freezer (the `thaw` subcommand) reads the record.
	if err := (&Freezer{Ctl: ctl, StateDir: dir}).Thaw(context.Background()); err != nil || ctl.nFrozen() != 0 {
		t.Fatalf("err=%v frozen=%d", err, ctl.nFrozen())
	}
	if err := (&Freezer{Ctl: ctl, StateDir: dir}).Thaw(context.Background()); err != nil {
		t.Fatalf("second thaw: %v", err)
	}
}

func TestThawKeepsRecordOnRealFailure(t *testing.T) {
	ctl := newCtl(session()...)
	dir := t.TempDir()
	f := &Freezer{Ctl: ctl, StateDir: dir}
	if _, err := f.Freeze(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctl.thawErr = errors.New("busy")
	if err := f.Thaw(context.Background()); err == nil || !Asleep(dir) {
		t.Fatalf("err=%v asleep=%v", err, Asleep(dir))
	}
	ctl.thawErr = nil
	if err := f.Thaw(context.Background()); err != nil || Asleep(dir) {
		t.Fatalf("retry: err=%v asleep=%v", err, Asleep(dir))
	}
}

func TestParseShow(t *testing.T) {
	got := parseShow("ControlGroup=/a/b.service\nId=b.service\n\nControlGroup=/c/d.scope\nId=d.scope\n")
	want := []Unit{{"b.service", "/a/b.service"}, {"d.scope", "/c/d.scope"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}
