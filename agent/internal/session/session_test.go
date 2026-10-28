package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
)

func TestDecide(t *testing.T) {
	jobs := func(units ...string) func() ([]sysd.Job, error) {
		return func() ([]sysd.Job, error) {
			var js []sysd.Job
			for _, u := range units {
				js = append(js, sysd.Job{Unit: u})
			}
			return js, nil
		}
	}
	tests := []struct {
		name    string
		marker  string
		list    func() ([]sysd.Job, error)
		wantYes bool
	}{
		{"halt job", "", jobs("halt.target", "x.service"), true},
		{"suspend marker", "suspend\n", jobs(), true},
		{"poweroff only", "", jobs("poweroff.target"), false},
		{"no jobs", "", jobs(), false},
		{"list error", "", func() ([]sysd.Job, error) { return nil, errors.New("no bus") }, false},
		{"marker beats list error", "suspend", func() ([]sysd.Job, error) { return nil, errors.New("no bus") }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.marker != "" {
				os.WriteFile(filepath.Join(dir, "stop-reason"), []byte(tc.marker), 0o644)
			}
			if got, why := Decide(dir, tc.list); got != tc.wantYes || why == "" {
				t.Fatalf("got %v %q", got, why)
			}
		})
	}
}
