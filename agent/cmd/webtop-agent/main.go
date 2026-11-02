// Command webtop-agent is the in-pod agent of a desktop.
//
//	webtop-agent run                 long-running: boot/steady reports, and the gateway's apps API on :8081
//	webtop-agent power-hook          at shutdown: report the queued target (poweroff|reboot|halt)
//	webtop-agent suspend             Sleep: freeze the session until the next client (or, with
//	                                 WEBTOP_SUSPEND_MODE=poweroff or on failure, mark + poweroff)
//	webtop-agent thaw                thaw whatever Sleep froze (idempotent)
//	webtop-agent save-session        save the Plasma session on kube stop/suspend
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/apps"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/boot"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/config"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/freeze"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/idle"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/powerhook"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/session"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/usersession"
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
	case "thaw":
		thaw(cfg)
	case "save-session":
		session.Run(cfg.StateDir)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: webtop-agent run | power-hook | suspend | thaw | save-session")
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

// suspend blocks while the session is frozen and returns 0 on wake, so systemd and logind
// run their normal resume. If freezing is off or fails, it powers off like a suspend used to.
func suspend(cfg config.Config) int {
	if cfg.SuspendMode == "freeze" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		err := (&freeze.Sleeper{
			Freezer:  newFreezer(cfg),
			Sessions: boot.SelkiesSessions(cfg.HTTPPort),
			Poll:     500 * time.Millisecond,
			Notify:   notifyAsleep(cfg),
		}).Run(ctx)
		if err == nil {
			return 0
		}
		log.Printf("suspend: freeze failed, powering off instead: %v", err)
	}
	return suspendPoweroff(cfg)
}

func newFreezer(cfg config.Config) *freeze.Freezer {
	return &freeze.Freezer{Ctl: freeze.UserCtl{User: usersession.LookupUser(cfg.User)}, StateDir: cfg.StateDir}
}

// notifyAsleep reports the sleep state once; the steady reports repeat it from the state file.
func notifyAsleep(cfg config.Config) func(bool) {
	client := api.New(cfg.ControllerURL, cfg.TokenPath)
	return func(asleep bool) {
		if !client.Enabled() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Report(ctx, api.Report{Asleep: &asleep}); err != nil {
			log.Printf("suspend: report asleep=%v: %v", asleep, err)
		}
	}
}

func thaw(cfg config.Config) {
	if err := newFreezer(cfg).Thaw(context.Background()); err != nil {
		log.Printf("thaw: %v", err) // never block the shutdown
	}
}

func suspendPoweroff(cfg config.Config) int {
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
	go serveApps(ctx, cfg)
	if cfg.ControllerURL == "" {
		// Wait instead of exiting so Restart=always doesn't loop.
		log.Print("run: WEBTOP_CONTROLLER_URL not set, API disabled")
		<-ctx.Done()
		return
	}
	client := api.New(cfg.ControllerURL, cfg.TokenPath)
	// Idle detection is best effort and independent of readiness.
	tracker := idle.NewTracker(cfg.StateDir)
	du := usersession.LookupUser(cfg.User)
	// What the account is (passwd, after the rename at activation), not what webtop-init asked for.
	client.LoginName, client.HomePaths = du.Name, cfg.HomePaths
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
		Asleep:   func() bool { return freeze.Asleep(cfg.StateDir) },
		Poll:     5 * time.Second,
		Interval: 30 * time.Second,
	}).Run(ctx)
}

// serveApps serves the gateway-facing apps API (docs/contract.md, 7.7) until ctx ends, independently
// of the controller API.
func serveApps(ctx context.Context, cfg config.Config) {
	u := usersession.LookupUser(cfg.User)
	l := &apps.Launcher{Session: u, StateDir: cfg.StateDir, Home: u.Home}
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", apps.Port),
		Handler:           apps.Handler(l),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("apps API: %v", err) // the agent's other jobs go on; systemd restarts it on exit only
	}
}
