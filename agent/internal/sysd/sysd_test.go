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
