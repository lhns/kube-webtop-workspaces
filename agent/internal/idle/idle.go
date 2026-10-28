// Package idle tracks input idleness from the compositor and mirrors it to
// <state>/idle-since for the power hook.
package idle

import (
	"context"
	"log"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/wayland"
)

// notifyTimeout is the idle timeout registered with the compositor; the
// idle start is backdated by it.
const notifyTimeout = time.Second

type Tracker struct {
	file string
	Now  func() time.Time

	mu    sync.Mutex
	since time.Time // zero = not idle
}

func NewTracker(stateDir string) *Tracker {
	t := &Tracker{file: filepath.Join(stateDir, state.IdleSince), Now: time.Now}
	t.Set(false)
	return t
}

// Set records the idle state and writes the file (unix seconds, or empty).
func (t *Tracker) Set(idle bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var data []byte
	if idle {
		t.since = t.Now().Add(-notifyTimeout)
		data = []byte(strconv.FormatInt(t.since.Unix(), 10))
	} else {
		t.since = time.Time{}
	}
	if err := state.WriteAtomic(t.file, data); err != nil {
		log.Printf("idle: write %s: %v", t.file, err)
	}
}

// Seconds is the current idle time, 0 when not idle.
func (t *Tracker) Seconds() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.since.IsZero() {
		return 0
	}
	return max(0, int(t.Now().Sub(t.since).Seconds()))
}

// Watch follows the compositor until ctx ends, reconnecting after failures
// (KWin not up yet, or restarted). Failures only mean "not idle".
func (t *Tracker) Watch(ctx context.Context, socket func(context.Context) (string, error), retry time.Duration) {
	last, lastPath := "", ""
	for ctx.Err() == nil {
		path, err := socket(ctx) // rediscovered on every attempt: KWin may have restarted
		if err == nil {
			if path != lastPath {
				log.Printf("idle: watching %s", path)
				lastPath = path
			}
			err = wayland.Dial(ctx, path, t.Set)
		}
		if ctx.Err() != nil {
			return // keep idle-since: the power hook reads it during shutdown
		}
		t.Set(false)
		if msg := err.Error(); msg != last { // don't spam while KWin is down
			log.Printf("idle: wayland: %v (retrying every %v)", err, retry)
			last = msg
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}
