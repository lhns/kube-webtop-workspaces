// Package logout detects that the user's session ended without a shutdown, and reads the user's
// choice about signing out at the login provider. The gateway acts on it (docs/contract.md, 7.8).
package logout

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/powerhook"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
)

const (
	// ConfFile is the user's file below their home. The user writes it; the agent, which runs as
	// root, only ever reads two booleans from it.
	ConfFile = ".config/webtop/session.conf"
	maxConf  = 4096
)

// storeRoot is where Nix keeps its immutable, root-owned files; a variable for tests.
var storeRoot = "/nix/store/"

// Pref returns the user's onLogout choice, or nil if they made none. Everything else about the
// file is ignored: unknown keys, malformed lines, other values than true and false. A missing
// file, a non-regular one or one over 4 KiB counts as no file. Home-manager links the file into
// the Nix store, so links are resolved, and the result is read only if it is the file itself
// (the user's own, as before) or lies in the store, which the user cannot write. A link to
// anywhere else (the agent is root) counts as no file.
func Pref(home string) *bool {
	rh, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil
	}
	p, err := filepath.EvalSymlinks(filepath.Join(rh, ConfFile))
	if err != nil {
		return nil
	}
	if p != filepath.Join(rh, ConfFile) && !strings.HasPrefix(p, storeRoot) {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxConf {
		return nil
	}
	var pref *bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.TrimSpace(k) != "onLogout" {
			continue
		}
		switch strings.TrimSpace(v) {
		case "true":
			t := true
			pref = &t
		case "false":
			f := false
			pref = &f
		}
	}
	return pref
}

// SessionExit runs from ExecStopPost of the session unit. It records a logout when the session
// ended by itself: success, and no poweroff, reboot or halt job queued (those are shutdowns, which
// the power hook reports). env is os.Getenv; home is the desktop user's home.
func SessionExit(stateDir, home string, env func(string) string, listJobs func() ([]sysd.Job, error), now time.Time) (marked bool, err error) {
	if env("SERVICE_RESULT") != "success" {
		return false, nil
	}
	jobs, err := listJobs()
	if err != nil {
		return false, fmt.Errorf("cannot tell a logout from a shutdown: %w", err)
	}
	if powerhook.Instance(jobs) != "" {
		return false, nil
	}
	return true, Mark(stateDir, now, Pref(home))
}

// Mark writes the marker: a time and the user's choice (on, off or empty).
func Mark(stateDir string, at time.Time, pref *bool) error {
	p := ""
	if pref != nil {
		p = map[bool]string{true: "on", false: "off"}[*pref]
	}
	body := at.UTC().Format(time.RFC3339) + "\n" + p + "\n"
	return state.WriteAtomic(filepath.Join(stateDir, state.Logout), []byte(body))
}

// Read returns the marker, ok false if there is none or it is unreadable.
func Read(stateDir string) (at string, pref *bool, ok bool) {
	lines := strings.Split(state.Read(filepath.Join(stateDir, state.Logout)), "\n")
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[0])); err != nil {
		return "", nil, false
	}
	if len(lines) > 1 {
		switch strings.TrimSpace(lines[1]) {
		case "on":
			t := true
			pref = &t
		case "off":
			f := false
			pref = &f
		}
	}
	return strings.TrimSpace(lines[0]), pref, true
}
