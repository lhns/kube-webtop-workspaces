// Package usersession runs systemctl, busctl and other commands as the desktop user.
package usersession

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	systemctlTimeout = 5 * time.Second
	commandTimeout   = 20 * time.Second // a KWin script load is slower than a systemctl call
)

// User is the desktop user. systemctl runs as this user without PAM (unlike
// `systemctl -M user@.host`, which opens a login session per call) and talks
// to the user manager's private socket under XDG_RUNTIME_DIR.
type User struct {
	UID, GID uint32
	Name     string // login name; empty if unknown
	Home     string // empty if unknown
}

// LookupUser resolves name, or uid 1000 when name is empty (the image renames that account at
// boot), falling back to 1000:100.
func LookupUser(name string) User {
	u := User{UID: 1000, GID: 100}
	lookup := func() (*user.User, error) { return user.Lookup(name) }
	if name == "" {
		lookup = func() (*user.User, error) { return user.LookupId("1000") }
	}
	if r, err := lookup(); err == nil {
		if uid, err := strconv.ParseUint(r.Uid, 10, 32); err == nil {
			if gid, err := strconv.ParseUint(r.Gid, 10, 32); err == nil {
				u = User{UID: uint32(uid), GID: uint32(gid), Name: r.Username, Home: r.HomeDir}
			}
		}
	}
	return u
}

// Systemctl runs `systemctl --user args...` as the user, bounded by a timeout.
func (u User) Systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return u.run(ctx, "systemctl", args...)
}

// ActiveUnits lists the user manager's active services and scopes.
func (u User) ActiveUnits(ctx context.Context) ([]string, error) {
	out, err := u.Systemctl(ctx, "list-units", "--plain", "--no-legend", "--no-pager",
		"--type=service,scope", "--state=active")
	if err != nil {
		return nil, err
	}
	return parseUnitList(string(out)), nil
}

// parseUnitList takes the unit names from `systemctl list-units --plain --no-legend` output.
func parseUnitList(out string) []string {
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			names = append(names, f[0])
		}
	}
	return names
}

// Busctl runs `busctl --user args...` as the user, bounded by a timeout. The user
// bus is /run/user/<uid>/bus; running as the user avoids D-Bus auth as root.
func (u User) Busctl(ctx context.Context, args ...string) ([]byte, error) {
	return u.run(ctx, "busctl", args...)
}

func (u User) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return u.exec(ctx, systemctlTimeout, name, append([]string{"--user"}, args...)...)
}

// Command runs name as the user with the session bus in its environment, bounded by commandTimeout.
// Output is stdout; a failing command's stderr is in the returned *exec.ExitError.
func (u User) Command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return u.exec(ctx, commandTimeout, name, args...)
}

// sessionPath is the PATH of commands run as the desktop user.
const sessionPath = "/run/current-system/sw/bin:/usr/local/bin:/usr/bin:/bin"

// lookIn finds name in path. exec.Command searches the agent's own PATH, not cmd.Env's, and the
// agent's unit does not have the system profile on it. Not found: name, so exec reports the error.
func lookIn(path, name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return name
}

func (u User) exec(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, lookIn(sessionPath, name), args...)
	runtimeDir := "/run/user/" + strconv.FormatUint(uint64(u.UID), 10)
	cmd.Env = []string{
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus",
		"PATH=" + sessionPath,
	}
	setCredential(cmd, u.UID, u.GID)
	return cmd.Output()
}

// WaylandSocket finds KWin's socket: KWin exports WAYLAND_DISPLAY into the
// user manager's environment (Selkies runs its own compositor on another name).
func (u User) WaylandSocket(ctx context.Context) (string, error) {
	out, err := u.Systemctl(ctx, "show-environment")
	if err != nil {
		return "", err
	}
	return waylandSocketFromEnv(string(out), u.UID)
}

// waylandSocketFromEnv extracts the socket path from `systemctl show-environment` output.
func waylandSocketFromEnv(out string, uid uint32) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "WAYLAND_DISPLAY="); ok && v != "" {
			if path.IsAbs(v) {
				return v, nil
			}
			return "/run/user/" + strconv.FormatUint(uint64(uid), 10) + "/" + v, nil
		}
	}
	return "", errors.New("WAYLAND_DISPLAY not in the user manager environment")
}
