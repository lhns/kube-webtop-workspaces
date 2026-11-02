//go:build linux

package apps

import (
	"os"

	"golang.org/x/sys/unix"
)

// openNoSymlinks opens an already symlink-resolved path and fails if any component is a symlink,
// so a swap after the guard check cannot redirect the root-owned agent (the user controls
// ~/.local/share).
func openNoSymlinks(p string) (*os.File, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, p, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, &os.PathError{Op: "openat2", Path: p, Err: err}
	}
	return os.NewFile(uintptr(fd), p), nil
}
