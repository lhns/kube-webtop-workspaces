package sysd

import (
	"context"
	"net"
	"testing"
)

func TestParseBusctlBool(t *testing.T) {
	tests := map[string]bool{
		"b true\n":     true,
		"b false\n":    false,
		"s \"true\"\n": false,
		"b\n":          false,
		"":             false,
		"b true extra": false,
	}
	for in, want := range tests {
		if got := parseBusctlBool([]byte(in)); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

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

func TestPortOpen(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &Probe{Port: l.Addr().(*net.TCPAddr).Port}
	if !p.PortOpen(context.Background()) {
		t.Fatal("open port reported closed")
	}
	l.Close()
	if p.PortOpen(context.Background()) {
		t.Fatal("closed port reported open")
	}
}
