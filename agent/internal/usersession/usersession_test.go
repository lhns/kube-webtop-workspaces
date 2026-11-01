package usersession

import "testing"

func TestWaylandSocketFromEnv(t *testing.T) {
	tests := []struct {
		name, env, want string
		err             bool
	}{
		{"relative", "PATH=/bin\nWAYLAND_DISPLAY=wayland-0\n", "/run/user/1000/wayland-0", false},
		{"absolute", "WAYLAND_DISPLAY=/tmp/wl\n", "/tmp/wl", false},
		{"crlf", "WAYLAND_DISPLAY=wayland-1\r\n", "/run/user/1000/wayland-1", false},
		{"empty value", "WAYLAND_DISPLAY=\n", "", true},
		{"missing", "DISPLAY=:0\n", "", true},
	}
	for _, tc := range tests {
		got, err := waylandSocketFromEnv(tc.env, 1000)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("%s: got %q, %v", tc.name, got, err)
		}
	}
}

func TestParseUnitList(t *testing.T) {
	out := "app-firefox@1.service loaded active running Firefox\n\nplasma-kwin_wayland.service loaded active running KWin\n"
	if got := parseUnitList(out); len(got) != 2 || got[0] != "app-firefox@1.service" || got[1] != "plasma-kwin_wayland.service" {
		t.Fatalf("%q", got)
	}
}
