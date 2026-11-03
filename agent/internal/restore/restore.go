// Package restore reads the progress of the software restore after an image update, which the
// restore scripts write (image/webtop-restore-progress.sh), and shows it as a KDE job.
package restore

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// File is the progress file in the state directory.
const File = "restore-progress.json"

// Progress is the file's content. Steps are the reinstalled profile entries plus the
// home-manager switch.
type Progress struct {
	Active  bool   `json:"active"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"`
	Failed  bool   `json:"failed"`
	Message string `json:"message"`
}

// Read returns the progress. While no script has written the file yet, a pending reinstall
// (marker) counts as active, so the restore shows from the start of the boot. A file that says
// finished wins over the marker, which stays after a failed reinstall.
func Read(stateDir, marker string) Progress {
	b, err := os.ReadFile(filepath.Join(stateDir, File))
	if err == nil {
		var p Progress
		if json.Unmarshal(b, &p) == nil {
			p.Done, p.Total = max(p.Done, 0), max(p.Total, 0)
			return p
		}
	}
	if marker != "" {
		if _, err := os.Stat(marker); err == nil {
			return Progress{Active: true, Current: "Preparing"}
		}
	}
	return Progress{}
}

// Percent is the share of steps done, 0 while the total is unknown.
func (p Progress) Percent() uint32 {
	if p.Total <= 0 {
		return 0
	}
	return uint32(min(p.Done, p.Total) * 100 / p.Total)
}
