package session

import "testing"

func TestCopyGroup(t *testing.T) {
	const src, dst = "[Session: saved by user]", "[Session: saved at previous logout]"
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"append", "[General]\na=1\n\n" + src + "\nx=1\ny=2\n\n[Other]\nz=3\n",
			"[General]\na=1\n\n" + src + "\nx=1\ny=2\n\n[Other]\nz=3\n" + dst + "\nx=1\ny=2\n\n", true},
		{"replace old group", "[A]\n" + dst + "\nold=1\n[B]\nb=1\n" + src + "\nx=1",
			"[A]\n[B]\nb=1\n" + src + "\nx=1\n" + dst + "\nx=1\n", true},
		{"src missing", "[A]\na=1\n" + dst + "\nold=1\n", "[A]\na=1\n" + dst + "\nold=1\n", false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		got, ok := CopyGroup(tc.in, src, dst)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q,%v want %q,%v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
