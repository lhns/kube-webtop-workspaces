// Package sysd probes systemd and the desktop for the boot phase.
package sysd

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	dest       = "org.freedesktop.systemd1"
	managerObj = "/org/freedesktop/systemd1"
	managerIf  = "org.freedesktop.systemd1.Manager"
)

// Probe implements boot.Probe against the system bus, systemctl and TCP.
type Probe struct {
	User User
	Port int

	conn *dbus.Conn // system bus
}

const callTimeout = 3 * time.Second

// System reads Manager.Progress and the state of multi-user.target.
// Every D-Bus call is bounded; on any error the connection is dropped.
func (p *Probe) System(ctx context.Context) (progress float64, multiUser bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if p.conn == nil {
		if p.conn, err = dbus.ConnectSystemBus(); err != nil {
			return 0, false, err
		}
	}
	defer func() {
		if err != nil { // reconnect next time
			p.conn.Close()
			p.conn = nil
		}
	}()
	mgr := p.conn.Object(dest, managerObj)
	v, err := property(ctx, mgr, managerIf, "Progress")
	if err != nil {
		return 0, false, err
	}
	progress, _ = v.Value().(float64)

	var unit dbus.ObjectPath
	if err = mgr.CallWithContext(ctx, managerIf+".LoadUnit", 0, "multi-user.target").Store(&unit); err != nil {
		return progress, false, err
	}
	st, err := property(ctx, p.conn.Object(dest, unit), dest+".Unit", "ActiveState")
	if err != nil {
		return progress, false, err
	}
	s, _ := st.Value().(string)
	return progress, s == "active", nil
}

func property(ctx context.Context, o dbus.BusObject, iface, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := o.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(&v)
	return v, err
}

// DesktopActive reports whether the user's plasma-workspace.target is active.
func (p *Probe) DesktopActive(ctx context.Context) bool {
	_, err := p.User.Systemctl(ctx, "is-active", "--quiet", "plasma-workspace.target")
	return err == nil
}

// Compositing reports whether KWin's Compositing.active is true. The type is
// deliberately not checked: it is qpainter on Plasma 6.7 and OpenGL on 6.8.
func (p *Probe) Compositing(ctx context.Context) bool {
	out, err := p.User.Busctl(ctx, "get-property", "org.kde.KWin", "/Compositor", "org.kde.kwin.Compositing", "active")
	return err == nil && parseBusctlBool(out)
}

// parseBusctlBool parses busctl's `b true` output.
func parseBusctlBool(out []byte) bool {
	f := strings.Fields(string(out))
	return len(f) == 2 && f[0] == "b" && f[1] == "true"
}

// PortOpen reports whether the streaming server accepts TCP connections.
func (p *Probe) PortOpen(ctx context.Context) bool {
	c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port)))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Job is a queued systemd job (ListJobs: id, unit, type, state, job path, unit path).
type Job struct {
	ID       uint32
	Unit     string
	Type     string
	State    string
	JobPath  dbus.ObjectPath
	UnitPath dbus.ObjectPath
}

// ListJobs returns the job queue from the system bus.
func ListJobs() ([]Job, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var jobs []Job
	if err := conn.Object(dest, managerObj).Call(managerIf+".ListJobs", 0).Store(&jobs); err != nil {
		return nil, fmt.Errorf("ListJobs: %w", err)
	}
	return jobs, nil
}
