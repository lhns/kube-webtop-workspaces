// Package state handles the small files the agent shares across its
// processes (stop-reason, idle-since) in the state directory.
package state

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	StopReason = "stop-reason"
	IdleSince  = "idle-since"
)

// WriteAtomic replaces path via temp file + rename so readers never see a partial write.
func WriteAtomic(path string, data []byte) error {
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
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
