package restore

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/godbus/dbus/v5"
)

// View is one job entry in the desktop's notification tray (org.kde.JobViewV3).
type View interface {
	Update(props map[string]any) error
	// Terminate ends the entry and closes the view.
	Terminate(code uint32, msg string) error
	// Close drops the view without ending the entry (the shell ends it with the connection).
	Close()
}

// Viewer opens job entries (org.kde.JobViewServerV2).
type Viewer interface {
	Request() (View, error)
}

// Tracker mirrors the progress into one job entry: opened when the restore turns active, ended
// when it stops. Request failures (the shell is not up yet) are retried by the next Step.
type Tracker struct {
	Viewer Viewer
	view   View
	last   Progress
}

// Step applies the current progress.
func (t *Tracker) Step(p Progress) {
	switch {
	case p.Active:
		if t.view == nil {
			v, err := t.Viewer.Request()
			if err != nil {
				log.Printf("restore job: %v", err)
				return
			}
			t.view, t.last = v, Progress{}
		}
		if p == t.last {
			return
		}
		info := p.Current
		if p.Total > 0 {
			info = fmt.Sprintf("%s (%d of %d)", p.Current, min(p.Done+1, p.Total), p.Total)
		}
		if err := t.view.Update(map[string]any{
			"title": "Restoring your software after an update", "infoMessage": info, "percent": p.Percent(),
		}); err != nil {
			// The shell restarted (the view is gone) or hangs: open a new view next time.
			log.Printf("restore job: %v", err)
			t.view.Close()
			t.view = nil
			return
		}
		t.last = p
	case t.view != nil:
		var err error
		if p.Failed {
			msg := p.Message
			if msg == "" {
				msg = "Some of your software could not be restored."
			}
			err = t.view.Terminate(1, msg)
		} else {
			err = t.view.Terminate(0, "")
		}
		if err != nil {
			log.Printf("restore job: %v", err)
		}
		t.view = nil
	}
}

// Run polls until ctx ends.
func (t *Tracker) Run(ctx context.Context, read func() Progress, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		t.Step(read())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

const (
	service = "org.kde.JobViewServer"
	path    = "/JobViewServer"
	// A hung shell must not stall the tracker; godbus calls have no timeout of their own.
	callTimeout = 10 * time.Second
)

// call invokes method without D-Bus activation: only a running shell shows jobs.
func call(obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return obj.CallWithContext(ctx, method, dbus.FlagNoAutoStart, args...)
}

// DBusViewer talks to the shell on the session bus (the caller runs as the desktop user).
type DBusViewer struct {
	Desktop string // desktop entry of the "application" that owns the job
}

func (d *DBusViewer) Request() (View, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	var obj dbus.ObjectPath
	// Capabilities 0: neither suspendable nor killable. "immediate" shows it without the delay.
	c := call(conn.Object(service, path), "org.kde.JobViewServerV2.requestView",
		d.Desktop, int32(0), map[string]dbus.Variant{"immediate": dbus.MakeVariant(true)})
	if err := c.Store(&obj); err != nil {
		conn.Close()
		return nil, fmt.Errorf("requestView: %w", err)
	}
	return &dbusView{conn: conn, obj: conn.Object(service, obj)}, nil
}

type dbusView struct {
	conn *dbus.Conn
	obj  dbus.BusObject
}

func (v *dbusView) Update(props map[string]any) error {
	m := make(map[string]dbus.Variant, len(props))
	for k, val := range props {
		m[k] = dbus.MakeVariant(val)
	}
	return call(v.obj, "org.kde.JobViewV3.update", m).Err
}

func (v *dbusView) Terminate(code uint32, msg string) error {
	defer v.Close()
	return call(v.obj, "org.kde.JobViewV3.terminate", code, msg, map[string]dbus.Variant{}).Err
}

func (v *dbusView) Close() { v.conn.Close() }
