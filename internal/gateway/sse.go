// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

type statusEvent struct {
	Phase        string `json:"phase"`
	Stage        string `json:"stage"`
	BootProgress int32  `json:"bootProgress"`
	Message      string `json:"message"`
	Authorized   bool   `json:"authorized"`
}

func eventOf(d *v1alpha1.Desktop) statusEvent {
	if d == nil {
		return statusEvent{Authorized: true}
	}
	return statusEvent{
		Phase: string(d.Status.Phase), Stage: d.Status.Stage, BootProgress: d.Status.BootProgress,
		Message: d.Status.Message, Authorized: !authorizedFalse(d),
	}
}

// isReady matches the proxy condition, so a "ready" reload never lands on a boot page.
func isReady(d *v1alpha1.Desktop) bool {
	return d != nil && d.Status.Phase == v1alpha1.PhaseRunning && d.Status.URL != ""
}

// serveEvents streams desktop status as SSE. It never starts the desktop; d may be nil.
func (h *Handler) serveEvents(w http.ResponseWriter, r *http.Request, rt *route, d *v1alpha1.Desktop) {
	fl, ok := w.(http.Flusher)
	if !ok {
		h.errorPage(w, http.StatusInternalServerError, "Error", "Streaming is not supported.", outError)
		return
	}
	// Subscribe before the first send so a change in between is not missed.
	wake, cancel := h.hub.Subscribe(rt.name)
	defer cancel()

	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var last string
	// send writes a status event if it changed, then ready; false ends the stream.
	send := func(d *v1alpha1.Desktop) bool {
		b, _ := json.Marshal(eventOf(d))
		if s := string(b); s != last {
			last = s
			if _, err := fmt.Fprintf(w, "event: status\ndata: %s\n\n", b); err != nil {
				return false
			}
		}
		if isReady(d) {
			_, _ = fmt.Fprint(w, "event: ready\ndata: {}\n\n")
			fl.Flush()
			return false
		}
		fl.Flush()
		return true
	}
	if !send(d) {
		return
	}
	tick := time.NewTicker(h.ping)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case <-wake:
			cur, err := h.store.Get(r.Context(), rt.name)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					continue
				}
				cur = nil
			}
			if !send(cur) {
				return
			}
		}
	}
}
