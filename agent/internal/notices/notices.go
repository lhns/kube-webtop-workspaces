// Package notices keeps the desktop's notice files: webtop-nix shows each file of /run/webtop/notices
// as a notification (its ADR 0019). The only notice sent from here is update-pending.
package notices

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

// Subdir is webtop-nix's notice directory inside the state directory (/run/webtop).
const Subdir = "notices"

// UpdatePendingFile is the notice that follows the desktop's status.updatePending.
const UpdatePendingFile = "update-pending.json"

type notice struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Action string `json:"action"`
}

var updatePending = notice{
	Title:  "Updates are ready",
	Body:   "Restart your desktop to apply them.",
	Action: "restart",
}

// SetUpdatePending writes or removes update-pending.json in dir. It only touches the file when the
// state differs, so it can run on every report.
func SetUpdatePending(dir string, pending bool) error {
	path := filepath.Join(dir, UpdatePendingFile)
	if !pending {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(updatePending)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	// Created here too: an image without the notice watcher has no such directory, and a file in it is harmless.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return state.WriteAtomic(path, data)
}

// UpdatePendingFollower returns a func for api.Client.OnUpdatePending. Errors are logged once per change.
func UpdatePendingFollower(dir string) func(bool) {
	var mu sync.Mutex // reports come from the boot, steady and restore loops
	failed := false
	return func(pending bool) {
		mu.Lock()
		defer mu.Unlock()
		err := SetUpdatePending(dir, pending)
		if err != nil && !failed {
			log.Printf("notices: update-pending: %v", err)
		}
		failed = err != nil
	}
}
