// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
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
	// PodUID tells a tab that watches a running desktop that the pod was replaced.
	PodUID string `json:"podUID,omitempty"`
	// Pull is set while the image is being downloaded.
	Pull *pullEvent `json:"pull,omitempty"`
}

// pullEvent is the boot page's download line. Elapsed is computed here, so a browser with a
// skewed clock still ticks from the right start. It changes on every send, which is fine: a
// send happens only on a wake, and the page restarts its ticker from it.
type pullEvent struct {
	ElapsedSeconds      int64 `json:"elapsedSeconds"`
	SizeBytes           int64 `json:"sizeBytes,omitempty"`
	LastDurationSeconds int64 `json:"lastDurationSeconds,omitempty"`
}

func eventOf(d *v1alpha1.Desktop, now time.Time) statusEvent {
	if d == nil {
		return statusEvent{Authorized: true}
	}
	ev := statusEvent{
		Phase: string(d.Status.Phase), Stage: stageText(d.Status.Stage), BootProgress: d.Status.BootProgress,
		Message: d.Status.Message, Authorized: !authorizedFalse(d), PodUID: d.Status.PodUID,
	}
	if ip := d.Status.ImagePull; ip != nil && d.Status.Stage == v1alpha1.StagePullingImage && ip.StartedAt != nil {
		ev.Pull = &pullEvent{
			ElapsedSeconds: max(0, int64(now.Sub(ip.StartedAt.Time).Seconds())),
			SizeBytes:      ip.LastSizeBytes, LastDurationSeconds: ip.LastDurationSeconds,
		}
	}
	return ev
}

// isReady matches the proxy condition, so a "ready" reload never lands on a boot page.
func isReady(d *v1alpha1.Desktop) bool {
	return d != nil && d.Status.Phase == v1alpha1.PhaseRunning && d.Status.URL != ""
}

// current re-reads the desktop: nil if it is gone, d on any other error.
func (h *Handler) current(ctx context.Context, name string, d *v1alpha1.Desktop) *v1alpha1.Desktop {
	cur, err := h.store.Get(ctx, name)
	switch {
	case err == nil:
		return cur
	case apierrors.IsNotFound(err):
		return nil
	}
	return d
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
	d = h.current(r.Context(), rt.name, d) // it may have changed since the caller read it

	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// watch keeps the stream open when the desktop is ready: a tab showing the desktop follows it.
	watch := r.URL.Query().Get("watch") != ""
	var last string
	// send writes a status event if it changed, then ready; false ends the stream.
	send := func(d *v1alpha1.Desktop) bool {
		b, _ := json.Marshal(eventOf(d, h.now()))
		if s := string(b); s != last {
			last = s
			if _, err := fmt.Fprintf(w, "event: status\ndata: %s\n\n", b); err != nil {
				return false
			}
		}
		if isReady(d) && !watch {
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
			d = h.current(r.Context(), rt.name, d)
			if !send(d) {
				return
			}
		}
	}
}

// stageText is the boot page's wording for the controller's stage values; the agent's
// free-text stages pass through.
func stageText(stage string) string {
	switch stage {
	case "":
		return "Starting"
	case v1alpha1.StageCreatingContainer:
		return "Creating container"
	case v1alpha1.StagePullingImage:
		return "Downloading image"
	case v1alpha1.StageAttachingVolume:
		return "Waiting for volume"
	}
	return stage
}
