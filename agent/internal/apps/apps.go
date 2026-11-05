// Package apps shows each desktop application on its own Selkies screen (webtop-nix, "One app per
// screen"): it places the app's windows on SELKIES-<n> and starts it in the user's session.
package apps

import (
	"context"
	"encoding/json"
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
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

const (
	// firstAppDisplay: display 2 is Selkies' own sidebar button.
	firstAppDisplay = 3
	// DefaultMaxScreens caps the apps shown at once (WEBTOP_MAX_APP_SCREENS).
	DefaultMaxScreens = 4
	// reserveTTL keeps a just-opened app's screen counted until its page has connected.
	reserveTTL = time.Minute
)

// NameRE is a desktop file id such as org.kde.dolphin or firefox.
var NameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var (
	ErrInvalidName = errors.New("invalid app name")
	ErrNotFound    = errors.New("no such application")
	// ErrPlace: KWin refused webtop-app-screen (or the session is not up).
	ErrPlace  = errors.New("placing the app on the screen failed")
	ErrLaunch = errors.New("starting the app failed")
	// ErrUnknownScreen: no app has this display number.
	ErrUnknownScreen = errors.New("no app on that screen")
)

// CapError: opening the app would exceed the cap. Open lists the ids of the apps on screen.
type CapError struct {
	Max  int
	Open []string
}

func (e *CapError) Error() string { return fmt.Sprintf("at most %d app screens", e.Max) }

type reservation struct {
	n  int
	at time.Time
}

// screenState is the app-screens file.
type screenState struct {
	Next int            `json:"next"`
	Apps map[string]int `json:"apps"`
}

func readScreens(p string) screenState {
	st := screenState{Apps: map[string]int{}}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Apps == nil {
		st.Apps = map[string]int{}
	}
	return st
}

func writeScreens(p string, st screenState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return state.WriteAtomic(p, b)
}

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

// Launcher serializes Open calls: a place/launch sequence must not interleave.
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
	// MaxScreens caps the apps shown at once; 0 means DefaultMaxScreens.
	MaxScreens int
	// Now overrides the clock (tests).
	Now func() time.Time

	mu       sync.Mutex
	reserved map[string]reservation
}

// Open shows name on its own screen and returns the screen's display number (the n of SELKIES-<n>):
//  1. a sticky number per app, never reused while the state file lives, so a reload or a restored
//     tab of one app cannot reach another;
//  2. a new screen is refused over the cap; an app that has a screen (or just got one) is
//     re-attaching and never refused;
//  3. place name on its screen first, so its window lands there as it opens (placing again is
//     harmless and also recovers a lost KWin script);
//  4. start it unless an active app-<name> unit of the user manager shows it already runs.
//
// An app that is not detected as running but has its own single-instance logic simply raises its
// existing window.
func (l *Launcher) Open(ctx context.Context, name string) (int, error) {
	name = strings.TrimSuffix(name, ".desktop")
	if !NameRE.MatchString(name) {
		return 0, ErrInvalidName
	}
	file, err := l.desktopFile(ctx, name)
	if err != nil {
		return 0, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	statePath := filepath.Join(l.StateDir, state.AppScreens)
	st := readScreens(statePath)
	live, err := l.liveScreens(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrPlace, err)
	}
	now := l.now()
	n, known := st.Apps[name]
	if !known || (!live[n] && !l.fresh(name, now)) {
		busy := map[int]bool{}
		for k := range live {
			busy[k] = true
		}
		for app, r := range l.reserved {
			if app != name && now.Sub(r.at) < reserveTTL {
				busy[r.n] = true
			}
		}
		if len(busy) >= l.maxScreens() {
			return 0, l.capError(st, busy)
		}
	}
	if !known {
		n = max(st.Next, firstAppDisplay)
		st.Apps[name], st.Next = n, n+1
	}
	if out, err := l.Session.Command(ctx, "webtop-app-screen", "place", name, "SELKIES-"+strconv.Itoa(n)); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrPlace, detail(err, out))
	}
	if !known {
		if err := writeScreens(statePath, st); err != nil {
			log.Printf("apps: record app screens: %v", err)
		}
	}
	if l.reserved == nil {
		l.reserved = map[string]reservation{}
	}
	l.reserved[name] = reservation{n, now}
	running, err := l.running(ctx, name)
	if err != nil {
		log.Printf("apps: cannot tell whether %s runs, starting it: %v", name, err)
	}
	if running {
		return n, nil
	}
	if out, err := l.Session.Command(ctx, "systemd-run", "--user", "--collect", "--quiet",
		"--description=webtop app "+name, "--", "kioclient", "exec", file); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrLaunch, detail(err, out))
	}
	return n, nil
}

// Focus raises a screen: "main" or the display number of an app known to the state file.
func (l *Launcher) Focus(ctx context.Context, screen string) error {
	target := "main"
	if screen != "main" {
		n, err := strconv.Atoi(screen)
		if err != nil {
			return ErrUnknownScreen
		}
		l.mu.Lock()
		st := readScreens(filepath.Join(l.StateDir, state.AppScreens))
		l.mu.Unlock()
		known := false
		for _, v := range st.Apps {
			known = known || v == n
		}
		if !known {
			return ErrUnknownScreen
		}
		target = "SELKIES-" + strconv.Itoa(n)
	}
	if out, err := l.Session.Command(ctx, "webtop-app-screen", "focus", target); err != nil {
		return fmt.Errorf("%w: %v", ErrPlace, detail(err, out))
	}
	return nil
}

func (l *Launcher) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Launcher) maxScreens() int {
	if l.MaxScreens > 0 {
		return l.MaxScreens
	}
	return DefaultMaxScreens
}

func (l *Launcher) fresh(app string, now time.Time) bool {
	r, ok := l.reserved[app]
	return ok && now.Sub(r.at) < reserveTTL
}

// liveScreens parses `webtop-app-screen screens`: one SELKIES-<n> per line.
func (l *Launcher) liveScreens(ctx context.Context) (map[int]bool, error) {
	out, err := l.Session.Command(ctx, "webtop-app-screen", "screens")
	if err != nil {
		return nil, detail(err, out)
	}
	live := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "SELKIES-"); ok {
			if n, err := strconv.Atoi(v); err == nil {
				live[n] = true
			}
		}
	}
	return live, nil
}

func (l *Launcher) capError(st screenState, busy map[int]bool) error {
	e := &CapError{Max: l.maxScreens(), Open: []string{}}
	for app, n := range st.Apps {
		if busy[n] {
			e.Open = append(e.Open, app)
		}
	}
	slices.Sort(e.Open)
	return e
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
