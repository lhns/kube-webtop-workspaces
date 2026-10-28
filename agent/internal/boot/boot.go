// Package boot implements the agent's two reporting phases: boot progress
// until everything is up, then periodic status.
package boot

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
)

// Sender delivers reports; *api.Client implements it.
type Sender interface {
	Report(ctx context.Context, r api.Report) error
}

// Probe observes the boot. Each method must be cheap and non-blocking for long.
type Probe interface {
	// System returns systemd's boot progress (0..1) and whether multi-user.target is active.
	System(ctx context.Context) (progress float64, multiUser bool, err error)
	DesktopActive(ctx context.Context) bool
	PortOpen(ctx context.Context) bool
}

const (
	maxStage = 64

	StageSystem    = "Starting system services"
	StagePlasma    = "Starting Plasma"
	StageStreaming = "Starting streaming server"
	StageReady     = "Ready"
)

// Compute returns the stage text, progress and readiness for the probed state.
func Compute(sysProgress float64, multiUser, plasma, port bool) (stage string, pct int, ready bool) {
	sys := 50
	if !multiUser {
		sys = 10 + int(math.Round(40*min(max(sysProgress, 0), 1)))
	}
	pct = sys
	if plasma {
		pct += 30
	}
	if port {
		pct += 20
	}
	switch {
	case !multiUser:
		stage = StageSystem
	case !plasma:
		stage = StagePlasma
	case !port:
		stage = StageStreaming
	default:
		return StageReady, 100, true
	}
	return stage, min(pct, 99), false
}

func truncate(s string) string {
	if len(s) > maxStage {
		return s[:maxStage]
	}
	return s
}

// Booter drives the boot phase. Tick and Retry are injectable for tests.
type Booter struct {
	Probe  Probe
	Sender Sender
	Tick   time.Duration // poll interval = minimum report spacing
	Retry  time.Duration // delay after a failed report
}

// Run polls until ready has been acknowledged by the controller, or ctx ends.
func (b *Booter) Run(ctx context.Context) {
	var (
		lastPct = -1
		best    = 0 // reported progress never goes backwards
		retryAt time.Time
	)
	t := time.NewTicker(b.Tick)
	defer t.Stop()
	for {
		sys, multiUser, err := b.Probe.System(ctx)
		plasma, port := b.Probe.DesktopActive(ctx), b.Probe.PortOpen(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("boot: systemd probe: %v", err)
			// Don't let a broken D-Bus block readiness once the visible parts are up.
			multiUser = plasma && port
		}
		stage, pct, ready := Compute(sys, multiUser, plasma, port)
		best = max(best, pct)
		pct = best

		// Every stage change raises pct, so a flapping probe neither lowers progress nor the stage.
		if pct > lastPct && time.Now().After(retryAt) {
			r := api.Report{Stage: truncate(stage), BootProgress: &pct}
			if ready {
				r.Ready = &ready
			}
			if err := b.Sender.Report(ctx, r); err != nil {
				log.Printf("boot: report %q %d%%: %v", stage, pct, err)
				retryAt = time.Now().Add(b.Retry)
			} else {
				lastPct = pct
				if ready {
					log.Print("boot: ready")
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
