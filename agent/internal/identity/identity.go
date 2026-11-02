// Package identity collects what the agent reports about the desktop user's account.
package identity

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

// MaxError bounds the failure reason in a report (the controller refuses longer ones).
const MaxError = 400

// Error is why the rename at activation failed (image/nixos/identity.nix); "" if it did not.
func Error(stateDir string) string {
	s := strings.Join(strings.Fields(state.Read(filepath.Join(stateDir, state.IdentityError))), " ")
	if utf8.RuneCountInString(s) > MaxError {
		s = string([]rune(s)[:MaxError])
	}
	return s
}

// HomePaths are the mounted paths plus the account's own home, which activation mounts itself
// when the rename failed and the account stayed `user`.
func HomePaths(paths []string, name string) []string {
	own := "/home/" + name
	if name == "" || slices.Contains(paths, own) {
		return paths
	}
	return append(slices.Clone(paths), own)
}
