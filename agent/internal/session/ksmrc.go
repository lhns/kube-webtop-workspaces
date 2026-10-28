package session

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

func ksmserverrc() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "ksmserverrc")
}

// promoteSession makes the restore group a copy of the saved group in ksmserverrc.
func promoteSession() error {
	path := ksmserverrc()
	in, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, ok := CopyGroup(string(in), savedGroup, restoreGroup)
	if !ok {
		log.Printf("save-session: %s has no %s group, leaving it alone", path, savedGroup)
		return nil
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := state.WriteAtomicMode(path, []byte(out), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// CopyGroup replaces the INI group dst with a copy of group src (a group runs
// from its header line to the next line starting with "["). Everything else is
// kept byte-identical. ok is false, and content unchanged, if src is missing.
func CopyGroup(content, src, dst string) (out string, ok bool) {
	const nl = "\n"
	var (
		b       strings.Builder
		copied  []string
		section string // header of the group we are in
	)
	for _, line := range strings.SplitAfter(content, nl) {
		if strings.HasPrefix(line, "[") {
			section = strings.TrimRight(line, "\r\n")
		}
		if section == src {
			copied = append(copied, line)
		}
		if section != dst {
			b.WriteString(line)
		}
	}
	if len(copied) == 0 {
		return content, false
	}
	if s := b.String(); s != "" && !strings.HasSuffix(s, nl) {
		b.WriteString(nl)
	}
	b.WriteString(dst + nl)
	for _, l := range copied[1:] {
		b.WriteString(l)
	}
	if !strings.HasSuffix(b.String(), nl) {
		b.WriteString(nl)
	}
	return b.String(), true
}
