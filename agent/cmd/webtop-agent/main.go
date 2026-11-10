// Command webtop-agent is the in-pod agent of a desktop.
//
//	webtop-agent run                 long-running: boot/steady reports, and the gateway's apps API on :8081
//	webtop-agent power-hook          at shutdown: report the queued target (poweroff|reboot|halt)
//	webtop-agent sleep-report STATE  sleep hook of webtop-nix (services.webtop.sleep.hooks): tell the
//	                                 controller the desktop is asleep or awake
//	webtop-agent session-exit        ExecStopPost of the session unit: record a logout (no shutdown queued)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/apps"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/boot"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/config"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/identity"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/idle"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/logout"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/powerhook"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/restore"
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
	case "sleep-report":
		sleepReport(cfg, args[1:])
	case "session-exit":
		sessionExit(cfg)
	case "restore-job":
		restoreJob(cfg)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: webtop-agent run | power-hook | sleep-report asleep|awake | restore-job | session-exit")
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

// sleepReport is the sleep hook of webtop-nix (services.webtop.sleep.hooks), run as root with
// "asleep" after the freeze and "awake" after the thaw. It reports the state once; the steady
// reports repeat it from /run/webtop/frozen-units. Best effort: it always exits 0.
func sleepReport(cfg config.Config, args []string) {
	if len(args) != 1 || (args[0] != "asleep" && args[0] != "awake") {
		log.Print("sleep-report: want asleep or awake")
		return
	}
	client := api.New(cfg.ControllerURL, cfg.TokenPath)
	if !client.Enabled() {
		return
	}
	asleep := args[0] == "asleep"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := client.Report(ctx, api.Report{Asleep: &asleep}); err != nil {
		log.Printf("sleep-report: report asleep=%v: %v", asleep, err)
	}
}

// sessionExit never fails the unit: ExecStopPost errors would mark the session failed.
func sessionExit(cfg config.Config) {
	marked, err := logout.SessionExit(cfg.StateDir, usersession.LookupUser(cfg.User).Home, os.Getenv, sysd.ListJobs, time.Now())
	if err != nil {
		log.Printf("session-exit: %v", err)
	}
	log.Printf("session-exit: logout recorded=%v", marked)
}

// restoreJob shows the software restore as a job in the desktop's tray. It runs as the desktop
// user, on whose session bus the shell is.
func restoreJob(cfg config.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	// The shell takes name and icon of the entry from this desktop file (image/nixos/hooks.nix).
	viewer := &restore.DBusViewer{Desktop: "webtop-restore"}
	defer viewer.Close()
	t := &restore.Tracker{Viewer: viewer}
	nixLog := &restore.NixLog{Path: filepath.Join(cfg.StateDir, restore.NixLogFile)}
	t.Run(ctx, func() restore.Progress {
		return nixLog.Apply(restore.Read(cfg.StateDir, cfg.RestoreMarker))
	}, 2*time.Second)
}

// watchRestore puts the restore progress into the reports, and sends one when it changes.
func watchRestore(ctx context.Context, cfg config.Config, client *api.Client) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		p := restore.Read(cfg.StateDir, cfg.RestoreMarker)
		if client.SetRestore(api.SoftwareRestore{Active: p.Active, Done: p.Done, Total: p.Total, Failed: p.Failed}) {
			if err := client.Report(ctx, api.Report{}); err != nil {
				log.Printf("restore: report: %v", err) // the next report carries it
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
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
	client.LoginName, client.HomePaths = du.Name, identity.HomePaths(cfg.HomePaths, du.Name)
	client.LoginError = identity.Error(cfg.StateDir)
	socket := du.WaylandSocket
	if cfg.WaylandSocket != "" {
		socket = func(context.Context) (string, error) { return cfg.WaylandSocket, nil }
	}
	go tracker.Watch(ctx, socket, 5*time.Second)
	go watchRestore(ctx, cfg, client)

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
		Sender:  client,
		Clients: boot.SelkiesClients(cfg.HTTPPort),
		Idle:    tracker.Seconds,
		Asleep:  func() bool { return state.Read(filepath.Join(cfg.StateDir, state.FrozenUnits)) != "" },
		Logout:  func() (string, *bool, bool) { return logout.Read(cfg.StateDir) },
		Lock: func() (string, *bool, bool) {
			at, ok := logout.ReadLock(fmt.Sprintf("/run/user/%d", du.UID))
			if !ok {
				return "", nil, false
			}
			return at, logout.LockPref(du.Home), true
		},
		Poll:     5 * time.Second,
		Interval: 30 * time.Second,
	}).Run(ctx)
}

// serveApps serves the gateway-facing apps API (docs/contract.md, 7.7) until ctx ends, independently
// of the controller API.
func serveApps(ctx context.Context, cfg config.Config) {
	u := usersession.LookupUser(cfg.User)
	l := &apps.Launcher{Session: u, StateDir: cfg.StateDir, Home: u.Home, MaxScreens: cfg.MaxAppScreens}
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
