package logout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
)

func writeConf(t *testing.T, home, body string) {
	t.Helper()
	p := filepath.Join(home, ConfFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func str(b *bool) string {
	if b == nil {
		return "nil"
	}
	return map[bool]string{true: "true", false: "false"}[*b]
}

func TestPref(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"on":              {"onLogout=true\n", "true"},
		"off with spaces": {"# c\n onLogout = false \n", "false"},
		"onLock ignored":  {"onLock=true\n", "nil"},
		"not a boolean":   {"onLogout=yes\nonLogout=1\n", "nil"},
		"a URL is no key": {"logoutUrl=https://evil.example\nonLogout=true\n", "true"},
		"garbage":         {"\x00\x01\xff\xfe=\n===\nonLogout\n", "nil"},
		"last valid wins": {"onLogout=true\nonLogout=maybe\n", "true"},
	} {
		home := t.TempDir()
		writeConf(t, home, tc.body)
		if got := str(Pref(home)); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
	if Pref(t.TempDir()) != nil {
		t.Error("no file must be no choice")
	}
}

func TestPrefRefusesBigFilesAndSymlinks(t *testing.T) {
	home := t.TempDir()
	writeConf(t, home, "onLogout=true\n"+strings.Repeat("#", maxConf))
	if Pref(home) != nil {
		t.Error("oversized file was read")
	}

	home = t.TempDir()
	real := filepath.Join(t.TempDir(), "elsewhere.conf")
	if err := os.WriteFile(real, []byte("onLogout=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config/webtop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(home, ConfFile)); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if Pref(home) != nil {
		t.Error("symlinked file was followed")
	}

	home = t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "session.conf"), []byte("onLogout=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(home, ".config/webtop")); err != nil {
		t.Skip(err)
	}
	if Pref(home) != nil {
		t.Error("symlinked directory was followed")
	}
}

func TestSessionExit(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	none := func() ([]sysd.Job, error) { return nil, nil }
	poweroff := func() ([]sysd.Job, error) { return []sysd.Job{{Unit: "poweroff.target"}}, nil }
	halt := func() ([]sysd.Job, error) { return []sysd.Job{{Unit: "halt.target"}}, nil }
	env := func(r string) func(string) string {
		return func(k string) string {
			if k == "SERVICE_RESULT" {
				return r
			}
			return ""
		}
	}
	for name, tc := range map[string]struct {
		result string
		jobs   func() ([]sysd.Job, error)
		want   bool
	}{
		"clean exit":    {"success", none, true},
		"poweroff":      {"success", poweroff, false},
		"pod deletion":  {"success", halt, false},
		"crash":         {"exit-code", none, false},
		"result absent": {"", none, false},
	} {
		dir, home := t.TempDir(), t.TempDir()
		writeConf(t, home, "onLogout=false\n")
		got, err := SessionExit(dir, home, env(tc.result), tc.jobs, now)
		if err != nil || got != tc.want {
			t.Errorf("%s: marked=%v err=%v", name, got, err)
		}
		at, pref, ok := Read(dir)
		if ok != tc.want || (ok && (at != "2026-01-02T03:04:05Z" || str(pref) != "false")) {
			t.Errorf("%s: marker %q %s %v", name, at, str(pref), ok)
		}
	}
}
