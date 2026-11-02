//go:build !linux

package apps

import "os"

// openNoSymlinks: the agent only runs on Linux; this keeps the package building elsewhere (tests).
func openNoSymlinks(p string) (*os.File, error) { return os.Open(p) }
