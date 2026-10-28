// Package sysd probes systemd and the desktop for the boot phase.
package sysd

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
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
	User string
	Port int

	conn *dbus.Conn
}

// System reads Manager.Progress and the state of multi-user.target.
func (p *Probe) System() (progress float64, multiUser bool, err error) {
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
	v, err := mgr.GetProperty(managerIf + ".Progress")
	if err != nil {
		return 0, false, err
	}
	progress, _ = v.Value().(float64)

	var unit dbus.ObjectPath
	if err = mgr.Call(managerIf+".LoadUnit", 0, "multi-user.target").Store(&unit); err != nil {
		return progress, false, err
	}
	st, err := p.conn.Object(dest, unit).GetProperty(dest + ".Unit.ActiveState")
	if err != nil {
		return progress, false, err
	}
	s, _ := st.Value().(string)
	return progress, s == "active", nil
}

// DesktopActive reports whether the user's plasma-workspace.target is active.
func (p *Probe) DesktopActive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "--user", "-M", p.User+"@.host", "is-active", "--quiet", "plasma-workspace.target").Run() == nil
}

// PortOpen reports whether the streaming server accepts TCP connections.
func (p *Probe) PortOpen() bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port)), time.Second)
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
