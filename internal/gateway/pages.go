// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"time"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

var pageTemplates = template.Must(template.ParseFS(templateFS, "templates/*.gohtml"))

type pageData struct {
	Title    string
	Message  string
	Stage    string
	Progress int32
	// EventsURL and StartURL are escaped paths; html/template escapes them per context.
	EventsURL string
	StartURL  string
	Reason    string
	StoppedAt time.Time // zero: not shown
	CSRF      string
}

// stopWording is the human wording of a lastStop reason.
func stopWording(r v1alpha1.StopReason) string {
	switch r {
	case v1alpha1.StopUser:
		return "It was shut down from the desktop."
	case v1alpha1.StopIdle:
		return "It was stopped because it was idle."
	case v1alpha1.StopSuspend:
		return "It was suspended."
	case v1alpha1.StopExit:
		return "It exited."
	case v1alpha1.StopAdmin:
		return "An administrator stopped it."
	case v1alpha1.StopLDAP:
		return "Your account is no longer allowed to use it."
	case v1alpha1.StopKubeStop:
		return "It was stopped by the cluster."
	default:
		return "It was stopped."
	}
}

// writePage renders a gateway-generated page. Everything the gateway generates is no-store.
func writePage(w http.ResponseWriter, status int, name string, data pageData) {
	var buf bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
