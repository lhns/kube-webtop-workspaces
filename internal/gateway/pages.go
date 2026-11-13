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
	Restore  bool // the boot page says the software is restored in the background
	// EventsURL and StartURL are escaped paths; html/template escapes them per context.
	EventsURL string
	StartURL  string
	WakeURL   string
	Reason    string
	StoppedAt time.Time // zero: not shown
	CSRF      string
	App       string // off and asleep pages: the app to return to after Start (appsOverview for the overview), empty for the desktop
	Apps      []appView
	Back      string // error page: a link back (escaped path), empty for none
	// RestartURL and StopURL are the error page's forms; CanRecover says whether to show them.
	RestartURL string
	StopURL    string
	CanRecover bool
	Wide       bool // the main card is wide (overview)
}

// appView is a card of the overview page.
type appView struct {
	ID      string
	Name    string
	Href    string
	IconURL string // empty: show Initial
	Initial string
	Running bool
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
	case v1alpha1.StopError:
		return "It was stopped after it failed to start."
	case v1alpha1.StopUnseen:
		return "It was stopped because it had not been used for a long time."
	default:
		return "It was stopped."
	}
}

// stageErrorSetup is the stage of an invalid name or a foreign volume (no pod involved).
const stageErrorSetup = "Error"

// failureStage is true for the stages the controller gives phase Error.
func failureStage(stage string) bool {
	switch stage {
	case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CrashLoopBackOff",
		"CreateContainerConfigError", "CreateContainerError", "RunContainerError", stageErrorSetup:
		return true
	}
	return false
}

// failureWording is the plain-words reason of an Error stage. Anything unknown gets the generic
// line, so what the cluster reports never reaches the page.
func failureWording(stage string) string {
	switch stage {
	case "ErrImagePull", "ImagePullBackOff", "InvalidImageName":
		return "The desktop image couldn't be downloaded."
	case "CrashLoopBackOff":
		return "The desktop keeps crashing while it starts."
	case "CreateContainerConfigError":
		return "The desktop's container couldn't be set up."
	case stageErrorSetup:
		return "Your desktop's name or storage couldn't be set up. An administrator has to fix this."
	}
	return "The desktop's container failed to start."
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
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
