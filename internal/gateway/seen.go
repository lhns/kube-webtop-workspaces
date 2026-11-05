// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

const (
	// lastSeenEvery is how often spec.lastSeen is written for a desktop whose owner keeps using it.
	lastSeenEvery = time.Hour
	// touchRetry spaces attempts per desktop, whatever the cache shows.
	touchRetry = 30 * time.Second
)

// displayName is the user's display name from the login proxy's header, cleaned. Header text is
// untrusted: it ends up in the passwd GECOS field, never in a page.
func (h *Handler) displayName(r *http.Request) string {
	if h.cfg.DisplayNameHeader == "" {
		return ""
	}
	return names.DisplayName(r.Header.Get(h.cfg.DisplayNameHeader))
}

// touch records that the owner was served, and the display name they sent, in the Desktop's
// spec: the gateway may not write status. A missing header leaves the stored name alone.
// Only authorized requests of the owner get here.
func (h *Handler) touch(ctx context.Context, d *v1alpha1.Desktop, name string) {
	now := h.now()
	stale := d.Spec.LastSeen == nil || now.Sub(d.Spec.LastSeen.Time) >= lastSeenEvery
	renamed := name != "" && name != d.Spec.DisplayName
	if !stale && !renamed {
		return
	}
	if last, ok := h.touched.Load(d.Name); ok && now.Sub(last.(time.Time)) < touchRetry {
		return
	}
	h.touched.Store(d.Name, now)
	if err := h.store.Touch(ctx, d, now, name); err != nil {
		h.log.Error(err, "recording last seen", "desktop", d.Name)
	}
}

// serveLogout sends the owner's browser to the login provider's logout URL. The URL lives in
// the gateway's config only: the page never contains it. It answers 404 when none is configured.
func (h *Handler) serveLogout(w http.ResponseWriter, r *http.Request, rt *route) {
	if h.cfg.LogoutURL == "" {
		h.errorPage(w, http.StatusNotFound, "Not found", "There is nothing here.", outNotFound)
		return
	}
	// Only a navigation from this site may sign the user out.
	if site := r.Header.Get("Sec-Fetch-Site"); !rt.owner || (site != "" && site != "same-origin") {
		h.errorPage(w, http.StatusForbidden, "Forbidden", "This request is not allowed.", outDenied)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.cfg.LogoutURL, http.StatusSeeOther)
}
