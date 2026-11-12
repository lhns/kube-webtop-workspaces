package notices

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdatePendingFollowsStatus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notices") // missing: created
	path := filepath.Join(dir, UpdatePendingFile)
	follow := UpdatePendingFollower(dir)

	follow(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no update pending, but the file exists: %v", err)
	}
	follow(true)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var n map[string]string
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	if n["title"] != "Updates are ready" || n["body"] != "Restart your desktop to apply them." || n["action"] != "restart" || len(n) != 3 {
		t.Fatalf("notice = %v", n)
	}
	// Unchanged state leaves the file alone (the watcher reacts to every write).
	before, _ := os.Stat(path)
	follow(true)
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("the file was rewritten")
	}
	// No temporary names are left behind.
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Fatalf("directory = %v", es)
	}
	follow(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the update is applied, but the file exists: %v", err)
	}
	follow(false) // removing nothing is fine
}
