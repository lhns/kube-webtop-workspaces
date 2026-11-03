package restore

import (
	"context"
	"errors"
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
	// Gone reports that the shell which owns the entry has exited, so it is lost.
	Gone() bool
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
		if t.view != nil && t.view.Gone() {
			t.view = nil // a new shell shows nothing of the old entry
		}
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
			info = fmt.Sprintf("%s (%d of %d)", info, min(p.Done+1, p.Total), p.Total)
		}
		if p.Detail != "" {
			info += ": " + p.Detail
		}
		if err := t.view.Update(map[string]any{
			"title": "Restoring your software after an update", "infoMessage": info, "percent": p.Percent(),
		}); err != nil {
			// Only a send error gets here (the bus is gone): open a new view next time.
			log.Printf("restore job: %v", err)
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

// Run polls until ctx ends. An entry still open then ends with the Viewer's connection.
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

// call invokes a method with a reply, without D-Bus activation: only a running shell shows jobs.
func call(obj dbus.BusObject, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return obj.CallWithContext(ctx, method, dbus.FlagNoAutoStart, args...)
}

// send is for the NoReply methods of org.kde.JobViewV3: the shell never answers them, so waiting
// would time out. Only a send error is reported.
func send(obj dbus.BusObject, method string, args ...any) error {
	return obj.Call(method, dbus.FlagNoReplyExpected|dbus.FlagNoAutoStart, args...).Err
}

// DBusViewer talks to the shell on the session bus (the caller runs as the desktop user). It
// keeps one connection: the shell fails the entries of a requester that disconnects.
type DBusViewer struct {
	Desktop string // desktop entry id of the "application" that owns the job
	conn    *dbus.Conn
}

func (d *DBusViewer) Request() (View, error) {
	if d.conn != nil && !d.conn.Connected() {
		d.Close() // the bus went away (session restart): dial again
	}
	if d.conn == nil {
		conn, err := dbus.ConnectSessionBus()
		if err != nil {
			return nil, fmt.Errorf("session bus: %w", err)
		}
		d.conn = conn
	}
	var obj dbus.ObjectPath
	// Capabilities 0: neither suspendable nor killable. "immediate" shows it without the delay.
	c := call(d.conn.Object(service, path), "org.kde.JobViewServerV2.requestView",
		d.Desktop, int32(0), map[string]dbus.Variant{"immediate": dbus.MakeVariant(true)})
	if err := c.Store(&obj); err != nil {
		return nil, fmt.Errorf("requestView: %w", err)
	}
	owner, err := d.owner()
	if err != nil {
		return nil, fmt.Errorf("requestView: %w", err)
	}
	return &dbusView{d: d, owner: owner, obj: d.conn.Object(owner, obj)}, nil
}

// Close ends the connection, and with it any entry still open.
func (d *DBusViewer) Close() {
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
	}
}

func (d *DBusViewer) owner() (string, error) {
	var owner string
	err := call(d.conn.BusObject(), "org.freedesktop.DBus.GetNameOwner", service).Store(&owner)
	return owner, err
}

type dbusView struct {
	d     *DBusViewer
	owner string // unique name of the shell that made the entry
	obj   dbus.BusObject
}

// Gone is true once the name has no owner or a new one; other errors count as alive.
func (v *dbusView) Gone() bool {
	owner, err := v.d.owner()
	var de dbus.Error
	if errors.As(err, &de) && de.Name == "org.freedesktop.DBus.Error.NameHasNoOwner" {
		return true
	}
	return err == nil && owner != v.owner
}

func (v *dbusView) Update(props map[string]any) error {
	m := make(map[string]dbus.Variant, len(props))
	for k, val := range props {
		m[k] = dbus.MakeVariant(val)
	}
	return v.sent(send(v.obj, "org.kde.JobViewV3.update", m))
}

func (v *dbusView) Terminate(code uint32, msg string) error {
	return v.sent(send(v.obj, "org.kde.JobViewV3.terminate", code, msg, map[string]dbus.Variant{}))
}

// sent drops a connection that failed to send, so the next Request dials again; no entry is
// live on a broken connection.
func (v *dbusView) sent(err error) error {
	if err != nil {
		v.d.Close()
	}
	return err
}
