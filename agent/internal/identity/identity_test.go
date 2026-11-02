package identity

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

func TestError(t *testing.T) {
	dir := t.TempDir()
	if got := Error(dir); got != "" {
		t.Fatalf("missing file: %q", got)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, state.IdentityError), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("usermod: user 'bob' already\nexists\n")
	if got := Error(dir); got != "usermod: user 'bob' already exists" {
		t.Fatalf("got %q", got)
	}
	write(strings.Repeat("é", MaxError+10))
	if got := Error(dir); len([]rune(got)) != MaxError {
		t.Fatalf("not bounded: %d runes", len([]rune(got)))
	}
}

func TestHomePaths(t *testing.T) {
	for _, tc := range []struct {
		paths []string
		name  string
		want  []string
	}{
		{[]string{"/home/bob", "/home/user"}, "bob", []string{"/home/bob", "/home/user"}},
		{[]string{"/home/bob"}, "user", []string{"/home/bob", "/home/user"}}, // rename failed
		{nil, "", nil},
	} {
		if got := HomePaths(tc.paths, tc.name); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v %q: %v, want %v", tc.paths, tc.name, got, tc.want)
		}
	}
}
