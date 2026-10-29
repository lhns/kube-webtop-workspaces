package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadMissingAndTrim(t *testing.T) {
	dir := t.TempDir()
	if got := Read(filepath.Join(dir, "none")); got != "" {
		t.Fatalf("missing = %q", got)
	}
	p := filepath.Join(dir, StopReason)
	os.WriteFile(p, []byte("suspend\n"), 0o644)
	if got := Read(p); got != "suspend" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteAtomicReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, IdleSince)
	for _, v := range []string{"first", "second"} {
		if err := WriteAtomic(p, []byte(v)); err != nil {
			t.Fatal(err)
		}
		if got := Read(p); got != v {
			t.Fatalf("got %q want %q", got, v)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestWriteAtomicMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	p := filepath.Join(t.TempDir(), "secret")
	if err := WriteAtomicMode(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
}

func TestWriteAtomicMissingDir(t *testing.T) {
	if err := WriteAtomic(filepath.Join(t.TempDir(), "no", "such", "f"), nil); err == nil {
		t.Fatal("expected error")
	}
}
