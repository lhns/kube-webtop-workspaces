// Package apps shows one desktop application on the second Selkies screen (webtop-nix, "One app
// per screen"): it places the app's windows on SELKIES-2 and starts it in the user's session.
package apps

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

// Screen is the KWin output of Selkies' display2 page.
const Screen = "SELKIES-2"

// NameRE is a desktop file id such as org.kde.dolphin or firefox.
var NameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var (
	ErrInvalidName = errors.New("invalid app name")
	ErrNotFound    = errors.New("no such application")
	// ErrPlace: KWin refused webtop-app-screen (or the session is not up).
	ErrPlace  = errors.New("placing the app on the screen failed")
	ErrLaunch = errors.New("starting the app failed")
)

// Port is where the agent serves the gateway (docs/contract.md 7.7). Also names.AgentPort in the
// controller module and $agentPort in the chart's networkpolicy.yaml: keep the three equal.
const Port = 8081

// Session runs commands as the desktop user. usersession.User implements it.
type Session interface {
	// Systemctl runs `systemctl --user args...`.
	Systemctl(ctx context.Context, args ...string) ([]byte, error)
	// ActiveUnits lists the user manager's active services and scopes.
	ActiveUnits(ctx context.Context) ([]string, error)
	// Command runs name with the session bus in its environment.
	Command(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Launcher serializes Open calls: a place/release/launch sequence must not interleave.
type Launcher struct {
	Session  Session
	StateDir string
	Home     string // the user's home, for ~/.local/share
	// Exists reports whether a file exists; nil uses the file system.
	Exists func(path string) bool
	// DataDirs overrides the data directories (tests); nil derives them from the user manager.
	DataDirs []string
	// StoreRoot is the Nix store whose files may be served (default /nix/store).
	StoreRoot string

	mu sync.Mutex
}

// Open makes name the app of the second screen:
//  1. release the previously placed app, if another one;
//  2. place name on Screen first, so its window lands there as it opens (placing again is harmless
//     and also recovers a lost KWin script);
//  3. start it unless an active app-<name> unit of the user manager shows it already runs.
//
// An app that is not detected as running but has its own single-instance logic simply raises its
// existing window.
func (l *Launcher) Open(ctx context.Context, name string) error {
	name = strings.TrimSuffix(name, ".desktop")
	if !NameRE.MatchString(name) {
		return ErrInvalidName
	}
	file, err := l.desktopFile(ctx, name)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	statePath := filepath.Join(l.StateDir, state.PlacedApp)
	if prev := state.Read(statePath); prev != "" && prev != name && NameRE.MatchString(prev) {
		if out, err := l.Session.Command(ctx, "webtop-app-screen", "release", prev); err != nil {
			log.Printf("apps: release %s: %v", prev, detail(err, out)) // the new app matters more
		}
	}
	if out, err := l.Session.Command(ctx, "webtop-app-screen", "place", name, Screen); err != nil {
		return fmt.Errorf("%w: %v", ErrPlace, detail(err, out))
	}
	if err := state.WriteAtomic(statePath, []byte(name+"\n")); err != nil {
		log.Printf("apps: record placed app: %v", err)
	}
	running, err := l.running(ctx, name)
	if err != nil {
		log.Printf("apps: cannot tell whether %s runs, starting it: %v", name, err)
	}
	if running {
		return nil
	}
	if out, err := l.Session.Command(ctx, "systemd-run", "--user", "--collect", "--quiet",
		"--description=webtop app "+name, "--", "kioclient", "exec", file); err != nil {
		return fmt.Errorf("%w: %v", ErrLaunch, detail(err, out))
	}
	return nil
}

func detail(err error, out []byte) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return fmt.Errorf("%w: %s", err, s)
	}
	return err
}

// desktopFile finds applications/<name>.desktop in the user's data directories.
func (l *Launcher) desktopFile(ctx context.Context, name string) (string, error) {
	exists := l.Exists
	if exists == nil {
		exists = func(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
	}
	for _, d := range l.dataDirList(ctx) {
		if p := path.Join(d, "applications", name+".desktop"); exists(p) {
			return p, nil
		}
	}
	return "", ErrNotFound
}

// dataDirs lists the user's data home, then XDG_DATA_DIRS of the user manager's environment
// (show-environment output), then the NixOS defaults.
func dataDirs(env, home string) []string {
	var dirs []string
	add := func(d string) {
		if path.IsAbs(d) {
			dirs = append(dirs, d)
		}
	}
	if home != "" {
		add(path.Join(home, ".local/share"))
	}
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_DATA_DIRS="); ok {
			for _, d := range strings.Split(v, ":") {
				add(d)
			}
		}
	}
	if home != "" {
		add(path.Join(home, ".nix-profile/share"))
		add("/etc/profiles/per-user/" + path.Base(home) + "/share")
	}
	add("/run/current-system/sw/share")
	add("/usr/share")
	return dirs
}

// running reports whether the user manager has an active unit of the app. KDE and systemd name
// them app-<id>@<random>.service or app-<id>-<pid>.scope, with "-" in the id escaped.
func (l *Launcher) running(ctx context.Context, name string) (bool, error) {
	units, err := l.Session.ActiveUnits(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(units, func(u string) bool { return unitIsApp(u, name) }), nil
}

var escapedRE = regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)

func unitIsApp(unit, name string) bool {
	rest, ok := strings.CutPrefix(unit, "app-")
	if !ok {
		return false
	}
	rest = escapedRE.ReplaceAllStringFunc(rest, func(m string) string {
		n, _ := strconv.ParseUint(m[2:], 16, 8)
		return string(rune(n))
	})
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, ".service"), ".scope")
	rest, ok = strings.CutPrefix(rest, name)
	if !ok {
		return false
	}
	if rest == "" || rest[0] == '@' {
		return true
	}
	_, err := strconv.ParseUint(strings.TrimPrefix(rest, "-"), 10, 64)
	return rest[0] == '-' && err == nil
}
