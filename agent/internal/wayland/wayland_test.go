package wayland

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestWireRoundTrip(t *testing.T) {
	args := new(Encoder).Uint(7).String("ext_idle_notifier_v1").String("ab").Uint(2).Bytes()
	raw := Message{Object: 2, Opcode: 3, Args: args}.Marshal()
	// "ab" => length 3, 3 bytes + 1 pad.
	if !bytes.Contains(raw, []byte{3, 0, 0, 0, 'a', 'b', 0, 0}) {
		t.Fatalf("string encoding wrong: %v", raw)
	}
	m, err := ReadMessage(bytes.NewReader(raw))
	if err != nil || m.Object != 2 || m.Opcode != 3 {
		t.Fatalf("%+v %v", m, err)
	}
	d := NewDecoder(m.Args)
	if d.Uint() != 7 || d.String() != "ext_idle_notifier_v1" || d.String() != "ab" || d.Uint() != 2 || d.Err != nil {
		t.Fatalf("decode failed: %v", d.Err)
	}
	d.Uint()
	if d.Err == nil {
		t.Fatal("expected short-argument error")
	}
}

func TestStringPaddingBoundary(t *testing.T) {
	// 3 chars + NUL = 4 bytes: no padding.
	if n := len(new(Encoder).String("abc").Bytes()); n != 8 {
		t.Fatalf("len = %d", n)
	}
	if n := len(new(Encoder).String("abcd").Bytes()); n != 12 {
		t.Fatalf("len = %d", n)
	}
}

func readMsg(t *testing.T, c net.Conn) Message {
	t.Helper()
	m, err := ReadMessage(c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func fakeCompositor(t *testing.T, c net.Conn, notifierVersion uint32, wantOp uint16) {
	defer c.Close()
	if m := readMsg(t, c); m.Object != 1 || m.Opcode != 1 || NewDecoder(m.Args).Uint() != 2 {
		t.Errorf("get_registry: %+v", m)
	}
	if m := readMsg(t, c); m.Object != 1 || m.Opcode != 0 || NewDecoder(m.Args).Uint() != 3 {
		t.Errorf("sync: %+v", m)
	}
	global := func(name uint32, iface string, ver uint32) {
		c.Write(Message{2, 0, new(Encoder).Uint(name).String(iface).Uint(ver).Bytes()}.Marshal())
	}
	global(1, "wl_compositor", 5)
	global(2, "wl_seat", 8)
	global(3, "ext_idle_notifier_v1", notifierVersion)
	c.Write(Message{1, 1, new(Encoder).Uint(3).Bytes()}.Marshal()) // delete_id: ignored
	c.Write(Message{3, 0, nil}.Marshal())                          // callback done

	bind := func(wantName uint32, wantIface string, wantVer, wantID uint32) {
		m := readMsg(t, c)
		d := NewDecoder(m.Args)
		name, iface, ver, id := d.Uint(), d.String(), d.Uint(), d.Uint()
		if m.Object != 2 || m.Opcode != 0 || name != wantName || iface != wantIface || ver != wantVer || id != wantID {
			t.Errorf("bind: %+v %d %q %d %d", m, name, iface, ver, id)
		}
	}
	bind(2, "wl_seat", 1, 4)
	bind(3, "ext_idle_notifier_v1", min(notifierVersion, 2), 5)
	m := readMsg(t, c)
	d := NewDecoder(m.Args)
	if m.Object != 5 || m.Opcode != wantOp || d.Uint() != 6 || d.Uint() != 1000 || d.Uint() != 4 {
		t.Errorf("get_*_idle_notification: %+v", m)
	}
	c.Write(Message{6, 0, nil}.Marshal()) // idled
	c.Write(Message{6, 1, nil}.Marshal()) // resumed
}

func TestWatch(t *testing.T) {
	for _, tc := range []struct {
		ver uint32
		op  uint16
	}{{1, 1}, {2, 2}, {3, 2}} {
		client, server := net.Pipe()
		go fakeCompositor(t, server, tc.ver, tc.op)
		var got []bool
		err := Watch(context.Background(), client, func(idle bool) { got = append(got, idle) })
		if err == nil { // server closes after resumed => read error
			t.Fatal("expected error on close")
		}
		if len(got) != 2 || !got[0] || got[1] {
			t.Fatalf("v%d events = %v", tc.ver, got)
		}
	}
}

func TestWatchMissingGlobal(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		readMsg(t, server)
		readMsg(t, server)
		server.Write(Message{3, 0, nil}.Marshal())
	}()
	if err := Watch(context.Background(), client, func(bool) {}); err == nil {
		t.Fatal("expected unsupported error")
	}
}

func TestWatchDisplayError(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		readMsg(t, server)
		readMsg(t, server)
		server.Write(Message{1, 0, new(Encoder).Uint(2).Uint(0).String("boom").Bytes()}.Marshal())
	}()
	if err := Watch(context.Background(), client, func(bool) {}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("boom")) {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchContextCancel(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- Watch(ctx, client, func(bool) {}) }()
	readMsg(t, server)
	readMsg(t, server)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return")
	}
}
