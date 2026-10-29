package idle

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

func newTracker(t *testing.T, now *time.Time) (*Tracker, string) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	tr.Now = func() time.Time { return *now }
	return tr, filepath.Join(dir, state.IdleSince)
}

func TestSetIdleBackdatesAndWritesFile(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr, file := newTracker(t, &now)
	if tr.Seconds() != 0 || state.Read(file) != "" {
		t.Fatal("new tracker should not be idle")
	}
	tr.Set(true)
	if got := state.Read(file); got != "999999" { // backdated by notifyTimeout
		t.Fatalf("file = %q", got)
	}
	now = now.Add(30 * time.Second)
	if s := tr.Seconds(); s != 31 {
		t.Fatalf("seconds = %d", s)
	}
	tr.Set(false)
	if tr.Seconds() != 0 || state.Read(file) != "" {
		t.Fatal("activity should clear idle")
	}
}

func TestSecondsNeverNegative(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr, _ := newTracker(t, &now)
	tr.Set(true)
	now = now.Add(-time.Hour) // clock stepped back
	if s := tr.Seconds(); s != 0 {
		t.Fatalf("seconds = %d", s)
	}
}

func TestWatchRediscoversSocketAndClearsIdle(t *testing.T) {
	now := time.Now()
	tr, _ := newTracker(t, &now)
	tr.Set(true)
	calls := make(chan struct{}, 10)
	socket := func(context.Context) (string, error) {
		calls <- struct{}{}
		return "", errors.New("no compositor")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { tr.Watch(ctx, socket, time.Millisecond); close(done) }()
	<-calls
	<-calls // rediscovered on every attempt
	cancel()
	<-done
	if tr.Seconds() != 0 {
		t.Fatal("failed watch should mean not idle")
	}
}

func TestWatchKeepsIdleSinceOnShutdown(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tr, file := newTracker(t, &now)
	tr.Set(true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr.Watch(ctx, func(context.Context) (string, error) { return "", errors.New("x") }, time.Millisecond)
	if state.Read(file) == "" {
		t.Fatal("idle-since must survive cancellation for the power hook")
	}
}
