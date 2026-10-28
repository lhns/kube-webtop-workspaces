// Package powerhook tells the controller why the desktop is shutting down.
//
// The reason is derived from the systemd target being started, since the
// container stop signal (SIGRTMIN+3) makes systemd start halt.target, while
// logind and `systemctl poweroff` start poweroff.target:
//
//	halt     -> stop/kube-stop   (pod deletion; the controller ignores it)
//	reboot   -> reboot
//	poweroff -> suspend  if <state>/stop-reason says "suspend"
//	            stop/idle  if the input-idle time (<state>/idle-since) is at least
//	                       the threshold: PowerDevil's idle shutdown fires after
//	                       a minute without input, whereas a user clicking
//	                       "Shut down" has just produced input
//	            stop/user  otherwise
package powerhook

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lhns/kube-webtop-workspaces/agent/internal/api"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
)

type Options struct {
	StateDir      string
	IdleThreshold time.Duration
	Timeout       time.Duration // total budget
	BackoffStart  time.Duration
	BackoffMax    time.Duration
	Now           func() time.Time
}

// Request maps the systemd target to the API payload; ok is false for unknown targets.
func Request(instance string, o Options) (req api.PowerRequest, ok bool) {
	switch instance {
	case "halt":
		return api.PowerRequest{Action: "stop", Reason: "kube-stop"}, true
	case "reboot":
		return api.PowerRequest{Action: "reboot"}, true
	case "poweroff":
		if state.Read(filepath.Join(o.StateDir, state.StopReason)) == "suspend" {
			return api.PowerRequest{Action: "suspend", Reason: "suspend"}, true
		}
		if secs, err := strconv.ParseInt(state.Read(filepath.Join(o.StateDir, state.IdleSince)), 10, 64); err == nil {
			if o.Now().Sub(time.Unix(secs, 0)) >= o.IdleThreshold {
				return api.PowerRequest{Action: "stop", Reason: "idle"}, true
			}
		}
		return api.PowerRequest{Action: "stop", Reason: "user"}, true
	}
	return api.PowerRequest{}, false
}

// Run posts the power request, retrying until success, a permanent error or
// the timeout. It only logs: the caller must exit 0 so shutdown never fails.
func Run(ctx context.Context, c *api.Client, instance string, o Options) {
	if !c.Enabled() {
		log.Print("power-hook: WEBTOP_CONTROLLER_URL not set, API disabled")
		return
	}
	req, ok := Request(instance, o)
	if !ok {
		log.Printf("power-hook: unknown instance %q, ignoring", instance)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	backoff := o.BackoffStart
	for attempt := 1; ; attempt++ {
		err := c.Power(ctx, req)
		if err == nil {
			log.Printf("power-hook: %+v acknowledged (attempt %d)", req, attempt)
			return
		}
		log.Printf("power-hook: attempt %d: %v", attempt, err)
		var se *api.StatusError
		if errors.As(err, &se) {
			switch se.Status {
			case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
				log.Printf("power-hook: permanent failure, giving up")
				return
			}
		}
		select {
		case <-ctx.Done():
			log.Printf("power-hook: timed out after %d attempts", attempt)
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, o.BackoffMax)
	}
}
