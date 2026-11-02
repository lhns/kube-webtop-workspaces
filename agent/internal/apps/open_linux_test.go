package apps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenNoSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := write(t, dir, "real", "x")
	if f, err := openNoSymlinks(real); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	// A symlink as the last component, or as a directory in the middle, is refused.
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "link"), filepath.Join(dir, "dirlink", "real")} {
		if f, err := openNoSymlinks(p); err == nil {
			f.Close()
			t.Errorf("%s: opened through a symlink", p)
		}
	}
}

// The file is swapped for a link to a root-only file after the guard approved it.
func TestSwapAfterCheckIsRefused(t *testing.T) {
	home, store := t.TempDir(), t.TempDir()
	l := &Launcher{Home: home, StoreRoot: store}
	secret := write(t, t.TempDir(), "secret.desktop", entry("Leaked", ""))
	file := write(t, home, ".local", "share", "applications", "x.desktop", entry("X", ""))
	g := l.newGuard()
	resolved, _, ok := g.resolve(file)
	if !ok {
		t.Fatal("guard refused a regular file in the user tree")
	}
	if err := os.Remove(resolved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, resolved); err != nil {
		t.Fatal(err)
	}
	if f, err := openNoSymlinks(resolved); err == nil {
		f.Close()
		t.Fatal("opened the swapped link")
	}
}
