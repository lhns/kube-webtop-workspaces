package boot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
)

func TestCompute(t *testing.T) {
	tests := []struct {
		sys              float64
		mu, plasma, port bool
		stage            string
		pct              int
		ready            bool
	}{
		{0, false, false, false, StageSystem, 10, false},
		{0.5, false, false, false, StageSystem, 30, false},
		{1, false, false, false, StageSystem, 50, false},
		{0.2, true, false, false, StagePlasma, 50, false},
		{0.2, true, true, false, StageStreaming, 80, false},
		{0.2, true, false, true, StagePlasma, 70, false},
		{0.2, true, true, true, StageReady, 100, true},
		{7, false, true, true, StageSystem, 99, false}, // clamped input
	}
	for _, tc := range tests {
		stage, pct, ready := Compute(tc.sys, tc.mu, tc.plasma, tc.port)
		if stage != tc.stage || pct != tc.pct || ready != tc.ready {
			t.Errorf("%+v: got %q %d %v", tc, stage, pct, ready)
		}
	}
}

type fakeProbe struct {
	mu                  sync.Mutex
	sys                 float64
	multiUser, dt, port bool
	err                 error
}

func (f *fakeProbe) set(fn func(*fakeProbe)) { f.mu.Lock(); fn(f); f.mu.Unlock() }
func (f *fakeProbe) System() (float64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sys, f.multiUser, f.err
}
func (f *fakeProbe) DesktopActive() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.dt }
func (f *fakeProbe) PortOpen() bool      { f.mu.Lock(); defer f.mu.Unlock(); return f.port }

type fakeSender struct {
	mu      sync.Mutex
	reports []api.Report
	fail    int // fail this many calls first
}

func (s *fakeSender) Report(_ context.Context, r api.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail > 0 {
		s.fail--
		return &api.StatusError{Status: 409}
	}
	s.reports = append(s.reports, r)
	return nil
}

func (s *fakeSender) snapshot() []api.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.Report(nil), s.reports...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestBootMonotonicAndOrdered(t *testing.T) {
	p := &fakeProbe{}
	s := &fakeSender{}
	b := &Booter{Probe: p, Sender: s, Tick: time.Millisecond, Retry: time.Millisecond}
	done := make(chan struct{})
	go func() { b.Run(context.Background()); close(done) }()

	step := func(fn func(*fakeProbe), wantLen int) {
		p.set(fn)
		waitFor(t, func() bool { return len(s.snapshot()) >= wantLen })
	}
	waitFor(t, func() bool { return len(s.snapshot()) >= 1 })
	step(func(f *fakeProbe) { f.sys = 0.5 }, 2)
	step(func(f *fakeProbe) { f.sys = 0.2 }, 2) // going back: nothing new reported
	step(func(f *fakeProbe) { f.multiUser = true }, 3)
	step(func(f *fakeProbe) { f.dt = true }, 4)
	step(func(f *fakeProbe) { f.dt = false }, 4) // plasma flaps: progress held
	step(func(f *fakeProbe) { f.dt, f.port = true, true }, 5)
	<-done

	got := s.snapshot()
	last := -1
	var stages []string
	for _, r := range got {
		if *r.BootProgress < last {
			t.Fatalf("progress went backwards: %+v", got)
		}
		last = *r.BootProgress
		if len(stages) == 0 || stages[len(stages)-1] != r.Stage {
			stages = append(stages, r.Stage)
		}
	}
	want := []string{StageSystem, StagePlasma, StageStreaming, StageReady}
	if strings.Join(stages, "|") != strings.Join(want, "|") {
		t.Fatalf("stages = %v", stages)
	}
	if r := got[len(got)-1]; r.Ready == nil || !*r.Ready || *r.BootProgress != 100 {
		t.Fatalf("last = %+v", r)
	}
}

func TestBootProbeErrorStillReady(t *testing.T) {
	p := &fakeProbe{err: context.DeadlineExceeded, dt: true, port: true}
	s := &fakeSender{}
	(&Booter{Probe: p, Sender: s, Tick: time.Millisecond, Retry: time.Millisecond}).Run(context.Background())
	if r := s.snapshot(); len(r) == 0 || r[len(r)-1].Ready == nil {
		t.Fatalf("reports = %+v", r)
	}
}

func TestReadyRetriedOn409(t *testing.T) {
	var calls atomic.Int32
	var last api.Report
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		mu.Lock()
		json.NewDecoder(r.Body).Decode(&last)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("t"), 0o644)

	p := &fakeProbe{multiUser: true, dt: true, port: true}
	b := &Booter{Probe: p, Sender: api.New(srv.URL, tok), Tick: time.Millisecond, Retry: time.Millisecond}
	b.Run(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 3 || last.Ready == nil || !*last.Ready || last.Stage != "Ready" {
		t.Fatalf("calls %d last %+v", calls.Load(), last)
	}
}

func TestSteadyClientChangeReportsImmediately(t *testing.T) {
	var clients atomic.Int32
	s := &fakeSender{}
	st := &Steady{
		Sender:   s,
		Clients:  func(context.Context) (int, error) { return int(clients.Load()), nil },
		Idle:     func() int { return 7 },
		Poll:     time.Millisecond,
		Interval: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)
	waitFor(t, func() bool { return len(s.snapshot()) == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := len(s.snapshot()); n != 1 {
		t.Fatalf("reports without change: %d", n)
	}
	clients.Store(2)
	waitFor(t, func() bool { return len(s.snapshot()) == 2 })
	r := s.snapshot()[1]
	if *r.ConnectedClients != 2 || *r.IdleSeconds != 7 || r.Ready != nil || r.Stage != "" {
		t.Fatalf("report = %+v", r)
	}
}

func TestSteadyPeriodicAndErrorKeepsLast(t *testing.T) {
	var fail atomic.Bool
	s := &fakeSender{}
	st := &Steady{
		Sender: s,
		Clients: func(context.Context) (int, error) {
			if fail.Load() {
				return 0, context.DeadlineExceeded
			}
			return 3, nil
		},
		Idle: func() int { return 0 }, Poll: time.Millisecond, Interval: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go st.Run(ctx)
	waitFor(t, func() bool { return len(s.snapshot()) >= 1 })
	fail.Store(true)
	waitFor(t, func() bool { return len(s.snapshot()) >= 3 }) // periodic reports continue
	for _, r := range s.snapshot() {
		if *r.ConnectedClients != 3 {
			t.Fatalf("clients = %d, want last known 3", *r.ConnectedClients)
		}
	}
}

func TestSelkiesClients(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"sessions":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(interface{ String() string }).String()
	port = port[strings.LastIndex(port, ":")+1:]
	n := 0
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	got, err := SelkiesClients(n)(context.Background())
	if err != nil || got != 2 {
		t.Fatalf("got %d, %v", got, err)
	}
}
