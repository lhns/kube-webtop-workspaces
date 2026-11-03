package restore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pending")
	if p := Read(dir, marker); p.Active {
		t.Fatalf("nothing running: %+v", p)
	}
	os.WriteFile(marker, nil, 0o644)
	if p := Read(dir, marker); !p.Active || p.Total != 0 {
		t.Fatalf("marker only: %+v", p)
	}
	os.MkdirAll(filepath.Dir(filepath.Join(dir, File)), 0o755)
	write := func(s string) { os.WriteFile(filepath.Join(dir, File), []byte(s), 0o644) }
	write(`{"active":true,"done":2,"total":5,"current":"Reinstalling x"}`)
	p := Read(dir, marker)
	if !p.Active || p.Done != 2 || p.Total != 5 || p.Percent() != 40 || p.Current != "Reinstalling x" {
		t.Fatalf("file: %+v", p)
	}
	// Finished beats the marker, which stays after a failed reinstall.
	write(`{"active":false,"done":2,"total":5,"failed":true,"message":"m"}`)
	if p := Read(dir, marker); p.Active || !p.Failed || p.Message != "m" {
		t.Fatalf("finished: %+v", p)
	}
	write(`{"active":true,"done":-3,"total":-1}`)
	if p := Read(dir, marker); p.Done != 0 || p.Total != 0 {
		t.Fatalf("negative: %+v", p)
	}
	write(`not json`)
	if p := Read(dir, marker); !p.Active {
		t.Fatalf("garbage falls back to the marker: %+v", p)
	}
}

type fakeView struct {
	updates []map[string]any
	ended   []string
	fail    bool // Update fails (the bus is gone)
	gone    bool // the shell that owns the entry exited
}

func (f *fakeView) Update(p map[string]any) error {
	if f.fail {
		return errors.New("connection closed")
	}
	f.updates = append(f.updates, p)
	return nil
}
func (f *fakeView) Gone() bool { return f.gone }
func (f *fakeView) Terminate(code uint32, msg string) error {
	f.ended = append(f.ended, string(rune('0'+code))+":"+msg)
	return nil
}

type fakeViewer struct {
	view *fakeView
	fail int
	n    int
}

func (f *fakeViewer) Request() (View, error) {
	f.n++
	if f.fail > 0 {
		f.fail--
		return nil, errors.New("no shell")
	}
	f.view = &fakeView{}
	return f.view, nil
}

func TestTrackerLifecycle(t *testing.T) {
	fv := &fakeViewer{fail: 1}
	tr := &Tracker{Viewer: fv}
	tr.Step(Progress{})
	if fv.n != 0 {
		t.Fatal("requested a view while idle")
	}
	tr.Step(Progress{Active: true, Current: "Preparing"})
	if fv.view != nil {
		t.Fatal("view despite the error")
	}
	tr.Step(Progress{Active: true, Current: "Preparing"}) // retried
	tr.Step(Progress{Active: true, Current: "Preparing"}) // unchanged: no update
	tr.Step(Progress{Active: true, Done: 1, Total: 4, Current: "Reinstalling a"})
	v := fv.view
	if len(v.updates) != 2 {
		t.Fatalf("updates: %v", v.updates)
	}
	u := v.updates[1]
	if u["percent"] != uint32(25) || u["infoMessage"] != "Reinstalling a (2 of 4)" || u["title"] == "" {
		t.Fatalf("update: %v", u)
	}
	tr.Step(Progress{Failed: true, Done: 1, Total: 4, Message: "boom"})
	tr.Step(Progress{Failed: true})
	if len(v.ended) != 1 || v.ended[0] != "1:boom" || fv.n != 2 {
		t.Fatalf("ended %v, requests %d", v.ended, fv.n)
	}
	tr.Step(Progress{Active: true})
	tr.Step(Progress{})
	if fv.view.ended[0] != "0:" {
		t.Fatalf("clean end: %v", fv.view.ended)
	}
}

func TestTrackerReopensAfterFailedUpdate(t *testing.T) {
	fv := &fakeViewer{}
	tr := &Tracker{Viewer: fv}
	tr.Step(Progress{Active: true, Current: "a"})
	first := fv.view
	first.fail = true
	tr.Step(Progress{Active: true, Current: "b"})
	tr.Step(Progress{Active: true, Current: "b"})
	if fv.n != 2 || fv.view == first || len(fv.view.updates) != 1 {
		t.Fatalf("not reopened: requests %d, updates %v", fv.n, fv.view.updates)
	}
}

func TestTrackerReopensWhenShellIsGone(t *testing.T) {
	fv := &fakeViewer{}
	tr := &Tracker{Viewer: fv}
	tr.Step(Progress{Active: true, Current: "a"})
	first := fv.view
	first.gone = true
	// Unchanged progress: the owner check alone triggers the new request.
	tr.Step(Progress{Active: true, Current: "a"})
	if fv.n != 2 || fv.view == first || len(fv.view.updates) != 1 || len(first.ended) != 0 {
		t.Fatalf("requests %d, updates %v, old ended %v", fv.n, fv.view.updates, first.ended)
	}
}

const nixSample = `{"action":"start","id":1,"level":3,"type":104,"text":""}
{"action":"result","id":1,"type":105,"fields":[0,4,0,0]}
{"action":"result","id":1,"type":106,"fields":[101,419430400]}
{"action":"start","id":2,"level":3,"type":103,"text":""}
{"action":"result","id":2,"type":105,"fields":[1,3,2,0]}
{"action":"start","id":3,"level":3,"type":101,"text":"downloading 'https://cache.nixos.org/x.nar.xz'"}
{"action":"result","id":3,"type":105,"fields":[104857600,209715200,0,0]}
`

func TestNixLogProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nix.jsonl")
	n := &NixLog{Path: path}
	if _, _, ok := n.Poll(); ok {
		t.Fatal("progress without a log")
	}
	os.WriteFile(path, []byte(nixSample+`{"action":"res`), 0o644) // last line still being written
	f, info, ok := n.Poll()
	if !ok || f != 1.0/7 || info != "Downloading 1/7 (100/400 MiB)" {
		t.Fatalf("%v %q %v", f, info, ok)
	}
	// The rest of the partial line, a finished transfer, then a build.
	extra := `ult","id":2,"type":105,"fields":[2,3,1,0]}
{"action":"stop","id":3}
{"action":"start","id":4,"level":3,"type":105,"text":"building '/nix/store/0123456789abcdfghijklmnpqrsvwxyz-firefox-1.0.drv'"}
`
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(extra)
	fh.Close()
	f, info, ok = n.Poll()
	if !ok || f != 2.0/7 || info != "Building firefox-1.0" {
		t.Fatalf("%v %q %v", f, info, ok)
	}
	// Truncated for a new run: back to indeterminate.
	os.WriteFile(path, nil, 0o644)
	if _, _, ok := n.Poll(); ok {
		t.Fatal("stale progress after truncation")
	}
}

func TestNixLogIgnoresGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nix.jsonl")
	os.WriteFile(path, []byte("not json\n@nix {\"action\":\"msg\"}\n{\"action\":\"result\",\"id\":9,\"type\":105,\"fields\":[1,2]}\n"), 0o644)
	if _, _, ok := (&NixLog{Path: path}).Poll(); ok {
		t.Fatal("progress from garbage")
	}
}

func TestPercentWithFraction(t *testing.T) {
	p := Progress{Active: true, Done: 1, Total: 4, Fraction: 0.5}
	if p.Percent() != 37 {
		t.Fatalf("%d", p.Percent())
	}
	if (Progress{Done: 4, Total: 4, Fraction: 0.5}).Percent() != 100 {
		t.Fatal("finished")
	}
	n := &NixLog{Path: filepath.Join(t.TempDir(), "none")}
	if q := n.Apply(Progress{Active: true}); q.Fraction != 0 || q.Detail != "" {
		t.Fatalf("fallback: %+v", q)
	}
}
