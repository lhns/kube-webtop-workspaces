// Command webtop-agent is the in-pod agent of a desktop.
//
//	webtop-agent run                 long-running: boot/steady reports
//	webtop-agent power-hook          at shutdown: report the queued target (poweroff|reboot|halt)
//	webtop-agent suspend             map suspend to "mark + start poweroff.target"
//	webtop-agent save-session        save the Plasma session on kube stop/suspend
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/boot"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/config"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/idle"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/powerhook"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/session"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
)

func main() {
	log.SetFlags(0) // the journal timestamps
	cfg := config.FromEnv(os.Getenv)
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
	}
	switch args[0] {
	case "run":
		run(cfg)
	case "power-hook":
		powerHook(cfg)
	case "suspend":
		os.Exit(suspend(cfg))
	case "save-session":
		session.Run(cfg.StateDir)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: webtop-agent run | power-hook | suspend | save-session")
	os.Exit(2)
}

func powerHook(cfg config.Config) {
	jobs, err := sysd.ListJobs()
	if err != nil {
		log.Printf("power-hook: cannot list jobs: %v", err)
		return
	}
	instance := powerhook.Instance(jobs)
	if instance == "" {
		log.Print("power-hook: no shutdown target queued, ignoring")
		return
	}
	powerhook.Run(context.Background(), api.New(cfg.ControllerURL, cfg.TokenPath), instance, powerhook.Options{
		StateDir:      cfg.StateDir,
		IdleThreshold: cfg.IdleThreshold,
		Timeout:       cfg.PowerHookTimeout,
		BackoffStart:  time.Second,
		BackoffMax:    5 * time.Second,
		Now:           time.Now,
	})
	// Always exit 0: a failing hook must never block the shutdown.
}

func suspend(cfg config.Config) int {
	if err := state.WriteAtomic(filepath.Join(cfg.StateDir, state.StopReason), []byte("suspend\n")); err != nil {
		log.Printf("suspend: %v", err)
		return 1
	}
	// Start the target via the manager: logind refuses poweroff while this
	// suspend operation is still in progress.
	cmd := exec.Command("systemctl", "start", "--no-block", "--job-mode=replace-irreversibly", "poweroff.target")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("suspend: systemctl start poweroff.target: %v", err)
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 1
	}
	return 0
}

func run(cfg config.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if cfg.ControllerURL == "" {
		// Wait instead of exiting so Restart=always doesn't loop.
		log.Print("run: WEBTOP_CONTROLLER_URL not set, API disabled")
		<-ctx.Done()
		return
	}
	client := api.New(cfg.ControllerURL, cfg.TokenPath)

	// Idle detection is best effort and independent of readiness.
	tracker := idle.NewTracker(cfg.StateDir)
	du := sysd.LookupUser(cfg.User)
	socket := du.WaylandSocket
	if cfg.WaylandSocket != "" {
		socket = func(context.Context) (string, error) { return cfg.WaylandSocket, nil }
	}
	go tracker.Watch(ctx, socket, 5*time.Second)

	(&boot.Booter{
		Probe:             &sysd.Probe{User: du, Port: cfg.HTTPPort},
		Sender:            client,
		Tick:              time.Second,
		Retry:             3 * time.Second,
		CompositorTimeout: cfg.CompositorTimeout,
	}).Run(ctx)
	if ctx.Err() != nil {
		return
	}
	(&boot.Steady{
		Sender:   client,
		Clients:  boot.SelkiesClients(cfg.HTTPPort),
		Idle:     tracker.Seconds,
		Poll:     5 * time.Second,
		Interval: 30 * time.Second,
	}).Run(ctx)
}
