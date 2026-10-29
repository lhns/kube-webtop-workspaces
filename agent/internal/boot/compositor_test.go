package boot

import (
	"context"
	"testing"
	"time"
)

func TestNotReadyWithoutCompositing(t *testing.T) {
	p := &fakeProbe{multiUser: true, dt: true, port: true}
	s := &fakeSender{}
	b := &Booter{Probe: p, Sender: s, Tick: time.Millisecond, Retry: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	waitFor(t, func() bool { return len(s.snapshot()) >= 1 })
	time.Sleep(30 * time.Millisecond)
	for _, r := range s.snapshot() {
		if r.Ready != nil || r.Stage != StageCompositor {
			t.Fatalf("report = %+v", r)
		}
	}
	p.set(func(f *fakeProbe) { f.comp = true })
	waitFor(t, func() bool { r := s.snapshot(); return r[len(r)-1].Ready != nil })
}

func TestCompositorStuckThenRecovers(t *testing.T) {
	p := &fakeProbe{multiUser: true, dt: true, port: true}
	s := &fakeSender{}
	b := &Booter{Probe: p, Sender: s, Tick: time.Millisecond, Retry: time.Millisecond, CompositorTimeout: 20 * time.Millisecond}
	done := make(chan struct{})
	go func() { b.Run(context.Background()); close(done) }()
	waitFor(t, func() bool { r := s.snapshot(); return len(r) > 0 && r[len(r)-1].Stage == StageCompositorStuck })
	time.Sleep(30 * time.Millisecond)
	n := 0
	for _, r := range s.snapshot() {
		if r.Stage == StageCompositorStuck {
			n++
		}
		if r.Ready != nil {
			t.Fatalf("ready while stuck: %+v", r)
		}
	}
	if n != 1 {
		t.Fatalf("stuck reported %d times, want once", n)
	}
	p.set(func(f *fakeProbe) { f.comp = true }) // recovery still completes the boot
	<-done
	if r := s.snapshot(); r[len(r)-1].Ready == nil {
		t.Fatalf("last = %+v", r[len(r)-1])
	}
}

func TestCompositorTimeoutDisabled(t *testing.T) {
	p := &fakeProbe{multiUser: true, dt: true, port: true}
	s := &fakeSender{}
	b := &Booter{Probe: p, Sender: s, Tick: time.Millisecond, Retry: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	time.Sleep(30 * time.Millisecond)
	for _, r := range s.snapshot() {
		if r.Stage == StageCompositorStuck {
			t.Fatalf("stuck reported with timeout 0: %+v", r)
		}
	}
}

func TestStagesFitLimit(t *testing.T) {
	for _, st := range []string{StageSystem, StagePlasma, StageStreaming, StageCompositor, StageCompositorStuck, StageReady} {
		if len(st) > maxStage {
			t.Errorf("%q exceeds %d", st, maxStage)
		}
	}
}
