package sysd

import (
	"context"
	"errors"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const systemctlTimeout = 5 * time.Second

// User is the desktop user. systemctl runs as this user without PAM (unlike
// `systemctl -M user@.host`, which opens a login session per call) and talks
// to the user manager's private socket under XDG_RUNTIME_DIR.
type User struct {
	UID, GID uint32
}

// LookupUser resolves name, falling back to 1000:100.
func LookupUser(name string) User {
	u := User{1000, 100}
	if r, err := user.Lookup(name); err == nil {
		if uid, err := strconv.ParseUint(r.Uid, 10, 32); err == nil {
			if gid, err := strconv.ParseUint(r.Gid, 10, 32); err == nil {
				u = User{uint32(uid), uint32(gid)}
			}
		}
	}
	return u
}

// Systemctl runs `systemctl --user args...` as the user, bounded by a timeout.
func (u User) Systemctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, systemctlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	cmd.Env = []string{
		"XDG_RUNTIME_DIR=/run/user/" + strconv.FormatUint(uint64(u.UID), 10),
		"PATH=/run/current-system/sw/bin:/usr/local/bin:/usr/bin:/bin",
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
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "WAYLAND_DISPLAY="); ok && v != "" {
			if filepath.IsAbs(v) {
				return v, nil
			}
			return "/run/user/" + strconv.FormatUint(uint64(u.UID), 10) + "/" + v, nil
		}
	}
	return "", errors.New("WAYLAND_DISPLAY not in the user manager environment")
}
