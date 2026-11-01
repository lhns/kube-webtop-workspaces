// Package state handles the small files in the state directory that the agent's processes share.
package state

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	StopReason = "stop-reason"
	IdleSince  = "idle-since"
	// FrozenUnits lists the units frozen by Sleep; it exists only while asleep.
	FrozenUnits = "frozen-units"
	// PlacedApp is the desktop file id placed on the second screen; empty or missing: none.
	PlacedApp = "placed-app"
)

// WriteAtomic replaces path via temp file + rename so readers never see a partial write.
func WriteAtomic(path string, data []byte) error { return WriteAtomicMode(path, data, 0o644) }

// WriteAtomicMode is WriteAtomic with an explicit file mode.
func WriteAtomicMode(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read returns the trimmed file content, or "" if it is missing.
func Read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
