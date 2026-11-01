// Package freeze implements Sleep for the desktop: it freezes the session's processes through
// the user manager's cgroup freezer, waits for the next web client, then thaws them.
//
// What was frozen is recorded in <state>/frozen-units so that any process (a SIGTERM
// handler, webtop-session-save) can thaw it; thawing is idempotent.
package freeze

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

// Unit is an active user unit and its cgroup path.
type Unit struct{ Name, CGroup string }

// Systemctl is the part of the user manager the freezer needs.
type Systemctl interface {
	Units(ctx context.Context) ([]Unit, error) // active services and scopes
	Freeze(ctx context.Context, units ...string) error
	Thaw(ctx context.Context, units ...string) error
}

// neverFrozen are kept running even where Select would take them: Selkies serves the wake-up
// connection and the readiness probe, the rest carries the session bus and audio it relies on.
var neverFrozen = []string{
	"selkies.service", "dbus-broker.service", "dbus.service", "at-spi-dbus-bus.service",
	"pipewire.service", "pipewire-pulse.service", "wireplumber.service",
}

// Select picks the units to freeze: plasma-*.service in session.slice and everything in
// app.slice (apps, dbus-activated services), minus neverFrozen. The user manager and
// background.slice are never candidates.
func Select(units []Unit) []string {
	var out []string
	for _, u := range units {
		if slices.Contains(neverFrozen, u.Name) {
			continue
		}
		switch sliceOf(u.CGroup) {
		case "app.slice":
		case "session.slice":
			if !strings.HasPrefix(u.Name, "plasma-") || !strings.HasSuffix(u.Name, ".service") {
				continue
			}
		default:
			continue
		}
		out = append(out, u.Name)
	}
	slices.Sort(out)
	return out
}

// sliceOf returns the top-level slice below user@<uid>.service in a cgroup path, or "".
func sliceOf(cgroup string) string {
	parts := strings.Split(strings.Trim(cgroup, "/"), "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "user@") && strings.HasSuffix(p, ".service") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// WakeRule decides from the Selkies session ids whether a client connected after Sleep.
// Wake on a new id, or on a rise of the count above the lowest count seen since Sleep: the
// first covers a reload (the tab reconnects under a new id), the second a closed and reopened
// tab and servers whose sessions carry no ids. Clients present at Sleep do not wake it.
type WakeRule struct {
	known map[string]bool
	low   int
}

func NewWakeRule(baseline []string) *WakeRule {
	w := &WakeRule{known: map[string]bool{}, low: len(baseline)}
	for _, id := range baseline {
		w.known[id] = true
	}
	return w
}

// Observe returns true when the sessions include a new connection.
func (w *WakeRule) Observe(ids []string) bool {
	n := len(ids)
	wake := n > w.low
	for _, id := range ids {
		if !w.known[id] {
			wake = true
		}
	}
	w.low = min(w.low, n)
	return wake
}

// Freezer freezes and thaws the session through Ctl.
type Freezer struct {
	Ctl      Systemctl
	StateDir string
}

func (f *Freezer) file() string { return filepath.Join(f.StateDir, state.FrozenUnits) }

// Asleep reports whether units are recorded as frozen.
func Asleep(stateDir string) bool {
	return state.Read(filepath.Join(stateDir, state.FrozenUnits)) != ""
}

func (f *Freezer) recorded() []string {
	return strings.Fields(state.Read(f.file()))
}

func (f *Freezer) record(units []string) error {
	if len(units) == 0 {
		err := os.Remove(f.file())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return state.WriteAtomic(f.file(), []byte(strings.Join(units, "\n")+"\n"))
}

// Freeze freezes the selected units. The list is recorded first, so a crash mid-way still
// leaves something to thaw. On any failure everything is thawed again.
func (f *Freezer) Freeze(ctx context.Context) ([]string, error) {
	all, err := f.Ctl.Units(ctx)
	if err != nil {
		return nil, fmt.Errorf("list units: %w", err)
	}
	units := Select(all)
	if len(units) == 0 {
		return nil, errors.New("no units to freeze")
	}
	if err := f.record(units); err != nil {
		return nil, fmt.Errorf("record frozen units: %w", err)
	}
	if err := f.Ctl.Freeze(ctx, units...); err != nil {
		// A unit that exited since the listing fails the batch; retry one by one.
		kept, err := f.each(ctx, units, f.Ctl.Freeze)
		if err != nil || len(kept) == 0 {
			if terr := f.Thaw(context.WithoutCancel(ctx)); terr != nil {
				log.Printf("freeze: thaw after failure: %v", terr)
			}
			return nil, errors.Join(errors.New("freeze failed"), err)
		}
		units = kept
		if err := f.record(units); err != nil {
			return nil, err
		}
	}
	return units, nil
}

// Thaw thaws every recorded unit and clears the record. Units that no longer exist are dropped;
// on a real failure the record keeps those units so a later Thaw retries.
func (f *Freezer) Thaw(ctx context.Context) error {
	units := f.recorded()
	if len(units) == 0 {
		return f.record(nil)
	}
	if err := f.Ctl.Thaw(ctx, units...); err == nil {
		return f.record(nil)
	}
	var failed []string
	for _, u := range units {
		if err := f.Ctl.Thaw(ctx, u); err != nil && f.active(ctx, u) {
			failed = append(failed, u)
		}
	}
	if err := f.record(failed); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not thaw %s", strings.Join(failed, ", "))
	}
	return nil
}

// each applies op to every unit separately, skipping units that are gone, and returns the
// ones it succeeded on.
func (f *Freezer) each(ctx context.Context, units []string, op func(context.Context, ...string) error) ([]string, error) {
	var ok []string
	var errs []error
	for _, u := range units {
		switch err := op(ctx, u); {
		case err == nil:
			ok = append(ok, u)
		case f.active(ctx, u):
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
		}
	}
	return ok, errors.Join(errs...)
}

func (f *Freezer) active(ctx context.Context, name string) bool {
	all, err := f.Ctl.Units(ctx)
	if err != nil {
		return true // cannot tell: treat the failure as real
	}
	return slices.ContainsFunc(all, func(u Unit) bool { return u.Name == name })
}

// Sleeper runs one sleep: freeze, wait for a new client, thaw.
type Sleeper struct {
	Freezer  *Freezer
	Sessions func(ctx context.Context) ([]string, error) // ids of the connected Selkies sessions
	Poll     time.Duration
	Notify   func(asleep bool) // best effort, must not block for long
}

// Run returns nil after a wake or after ctx ended (SIGTERM); both leave everything thawed. It
// returns an error, with nothing frozen, when it could not go to sleep, so the caller can fall
// back to a poweroff.
func (s *Sleeper) Run(ctx context.Context) error {
	baseline, err := s.Sessions(ctx)
	if err != nil {
		return fmt.Errorf("selkies sessions: %w", err)
	}
	rule := NewWakeRule(baseline)
	units, err := s.Freezer.Freeze(ctx)
	if err != nil {
		return err
	}
	log.Printf("freeze: asleep, %d units frozen, %d clients connected", len(units), len(baseline))
	s.Notify(true)

	t := time.NewTicker(s.Poll)
	defer t.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			log.Print("freeze: stopped while asleep, thawing")
			break wait
		case <-t.C:
		}
		ids, err := s.Sessions(ctx)
		if err == nil && rule.Observe(ids) {
			log.Print("freeze: client connected, waking")
			break wait
		}
	}
	// Not ctx: it is already cancelled on SIGTERM, and the thaw must happen before exit.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err = s.Freezer.Thaw(tctx)
	s.Notify(false)
	return err
}
