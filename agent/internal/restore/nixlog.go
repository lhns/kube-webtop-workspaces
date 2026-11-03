package restore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// NixLogFile is where the restore scripts point Nix's json-log-path setting (`webtop-restore-progress
// nixconfig`). Nix appends one JSON event per write, so concurrent nix processes do not interleave
// lines, and no listener has to exist before the first nix process starts.
const NixLogFile = "restore/nix.jsonl"

// Activity and result types of Nix's internal-json log (libutil logging.hh).
const (
	actCopyPath     = 100
	actFileTransfer = 101
	actCopyPaths    = 103
	actBuilds       = 104
	actBuild        = 105

	resProgress    = 105
	resSetExpected = 106
)

type activity struct {
	typ            int
	text           string
	done, expected uint64 // progress result
	setExpected    map[int]uint64
	order          int
}

// NixLog follows the events of the Nix processes of a restore and condenses them to one fraction
// and an info line, the way `nix`' own progress bar does.
type NixLog struct {
	Path string

	off      int64
	partial  []byte
	acts     map[uint64]*activity
	seq      int
	finished map[int]uint64 // bytes of stopped file transfers
}

// Reset forgets everything read so far.
func (n *NixLog) Reset() {
	n.off, n.partial, n.acts, n.finished, n.seq = 0, nil, nil, nil, 0
}

type nixEvent struct {
	Action string            `json:"action"`
	ID     uint64            `json:"id"`
	Type   int               `json:"type"`
	Text   string            `json:"text"`
	Fields []json.RawMessage `json:"fields"`
}

// Poll reads the events since the last call. ok is false while nothing live reports a total, so
// the caller keeps its indeterminate bar (legacy nix-* commands do not honor the setting).
func (n *NixLog) Poll() (fraction float64, info string, ok bool) {
	n.read()
	return n.summary()
}

func (n *NixLog) read() {
	f, err := os.Open(n.Path)
	if err != nil {
		n.Reset()
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() < n.off {
		n.Reset() // truncated or replaced: a new run
	}
	if _, err := f.Seek(n.off, io.SeekStart); err != nil {
		return
	}
	b, _ := io.ReadAll(f)
	n.off += int64(len(b))
	b = append(n.partial, b...)
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		n.apply(bytes.TrimPrefix(b[:i], []byte("@nix ")))
		b = b[i+1:]
	}
	n.partial = append([]byte(nil), b...)
}

func uints(fields []json.RawMessage) []uint64 {
	var out []uint64
	for _, f := range fields {
		var v uint64
		if json.Unmarshal(f, &v) != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func (n *NixLog) apply(line []byte) {
	var e nixEvent
	if json.Unmarshal(line, &e) != nil {
		return
	}
	if n.acts == nil {
		n.acts, n.finished = map[uint64]*activity{}, map[int]uint64{}
	}
	switch e.Action {
	case "start":
		n.seq++
		n.acts[e.ID] = &activity{typ: e.Type, text: e.Text, order: n.seq}
	case "stop":
		if a := n.acts[e.ID]; a != nil && a.typ == actFileTransfer {
			n.finished[actFileTransfer] += a.done
		}
		delete(n.acts, e.ID)
	case "result":
		a := n.acts[e.ID]
		if a == nil {
			return
		}
		v := uints(e.Fields)
		switch {
		case e.Type == resProgress && len(v) >= 2:
			a.done, a.expected = v[0], v[1]
		case e.Type == resSetExpected && len(v) >= 2:
			if a.setExpected == nil {
				a.setExpected = map[int]uint64{}
			}
			a.setExpected[int(v[0])] = v[1]
		}
	}
}

func (n *NixLog) summary() (float64, string, bool) {
	var done, expected, bytesDone, bytesExpected uint64
	var building *activity
	for _, a := range n.acts {
		switch a.typ {
		case actCopyPaths, actBuilds:
			done, expected = done+a.done, expected+a.expected
			bytesExpected += a.setExpected[actFileTransfer]
		case actFileTransfer:
			bytesDone += a.done
		case actBuild:
			if building == nil || a.order > building.order {
				building = a
			}
		}
	}
	if expected == 0 {
		return 0, "", false
	}
	bytesDone += n.finished[actFileTransfer]
	frac := min(float64(done)/float64(expected), 1)
	switch {
	case building != nil:
		return frac, "Building " + buildName(building.text), true
	case bytesExpected > 0:
		return frac, fmt.Sprintf("Downloading %d/%d (%d/%d MiB)", done, expected, bytesDone>>20, bytesExpected>>20), true
	}
	return frac, fmt.Sprintf("Downloading %d/%d", done, expected), true
}

// buildName is the derivation's name from "building '/nix/store/<hash>-name.drv'".
func buildName(text string) string {
	s := text
	if i := strings.Index(s, "'"); i >= 0 {
		s = strings.Trim(s[i:], "'")
	}
	s = strings.TrimSuffix(filepath.Base(s), ".drv")
	if len(s) > 33 && s[32] == '-' {
		s = s[33:]
	}
	return s
}

// Apply adds the Nix log's fraction and info to an active progress, and forgets the log once the
// restore is over.
func (n *NixLog) Apply(p Progress) Progress {
	if !p.Active {
		n.Reset()
		return p
	}
	if f, info, ok := n.Poll(); ok {
		p.Fraction, p.Detail = f, info
	}
	return p
}
