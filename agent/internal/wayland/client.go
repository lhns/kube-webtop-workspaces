package wayland

import (
	"context"
	"fmt"
	"net"
)

// Object ids are allocated by the client, starting after wl_display (1).
const (
	idDisplay      = 1
	idRegistry     = 2
	idSync         = 3
	idSeat         = 4
	idNotifier     = 5
	idNotification = 6

	idleTimeoutMs = 1000
)

const (
	ifaceSeat     = "wl_seat"
	ifaceNotifier = "ext_idle_notifier_v1"
)

// Dial connects to the compositor's unix socket and watches idleness.
func Dial(ctx context.Context, socket string, onIdle func(idle bool)) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	return Watch(ctx, conn, onIdle)
}

// Watch registers a 1 s input-idle notification and calls onIdle(true) on
// `idled` and onIdle(false) on `resumed`. It returns when the connection
// drops, the compositor reports an error, or ctx ends. It closes conn.
func Watch(ctx context.Context, conn net.Conn, onIdle func(idle bool)) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	send := func(obj uint32, op uint16, e *Encoder) error {
		_, err := conn.Write(Message{obj, op, e.Bytes()}.Marshal())
		return err
	}
	// wl_display.get_registry (opcode 1), then wl_display.sync (opcode 0) to
	// learn when all initial globals have been announced.
	if err := send(idDisplay, 1, new(Encoder).Uint(idRegistry)); err != nil {
		return err
	}
	if err := send(idDisplay, 0, new(Encoder).Uint(idSync)); err != nil {
		return err
	}

	type global struct{ name, version uint32 }
	globals := map[string]global{}
	for {
		m, err := ReadMessage(conn)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		d := NewDecoder(m.Args)
		switch {
		case m.Object == idDisplay && m.Opcode == 0: // error(object, code, message)
			obj, code, msg := d.Uint(), d.Uint(), d.String()
			return fmt.Errorf("wayland error on object %d (code %d): %s", obj, code, msg)
		case m.Object == idRegistry && m.Opcode == 0: // global(name, interface, version)
			name, iface, ver := d.Uint(), d.String(), d.Uint()
			if d.Err == nil {
				globals[iface] = global{name, ver}
			}
		case m.Object == idSync && m.Opcode == 0: // wl_callback.done
			seat, ok1 := globals[ifaceSeat]
			notifier, ok2 := globals[ifaceNotifier]
			if !ok1 || !ok2 {
				return fmt.Errorf("compositor lacks %s or %s", ifaceSeat, ifaceNotifier)
			}
			nver := min(notifier.version, 2)
			// wl_registry.bind (opcode 0) with the untyped new_id form.
			if err := send(idRegistry, 0, new(Encoder).Uint(seat.name).String(ifaceSeat).Uint(1).Uint(idSeat)); err != nil {
				return err
			}
			if err := send(idRegistry, 0, new(Encoder).Uint(notifier.name).String(ifaceNotifier).Uint(nver).Uint(idNotifier)); err != nil {
				return err
			}
			// v2 get_input_idle_notification ignores idle inhibitors, which is
			// what we want; v1 only has get_idle_notification.
			op := uint16(1)
			if nver >= 2 {
				op = 2
			}
			if err := send(idNotifier, op, new(Encoder).Uint(idNotification).Uint(idleTimeoutMs).Uint(idSeat)); err != nil {
				return err
			}
		case m.Object == idNotification && m.Opcode == 0:
			onIdle(true)
		case m.Object == idNotification && m.Opcode == 1:
			onIdle(false)
		}
		// delete_id and the other events carry nothing we need.
	}
}
