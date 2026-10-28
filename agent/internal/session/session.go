// Package session saves the Plasma session on kube stop or suspend.
package session

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/state"
	"github.com/lhns/kube-webtop-workspaces/agent/internal/sysd"
)

// The group named here is what loginMode=restorePreviousLogout restores;
// plain saveCurrentSession writes "saved by user" instead.
const (
	ksmService  = "org.kde.ksmserver"
	ksmPath     = "/KSMServer"
	saveMethod  = "org.kde.KSMServerInterface.saveCurrentSessionAs"
	sessionName = "saved at previous logout"

	saveTimeout = 30 * time.Second
)

// Decide returns whether to save and why. A KDE-initiated shutdown has
// already saved, and a shell `systemctl poweroff` doesn't save, like a real machine.
func Decide(stateDir string, listJobs func() ([]sysd.Job, error)) (save bool, why string) {
	if state.Read(filepath.Join(stateDir, state.StopReason)) == "suspend" {
		return true, "suspend"
	}
	jobs, err := listJobs()
	if err != nil {
		return false, "cannot list jobs: " + err.Error()
	}
	for _, j := range jobs {
		if j.Unit == "halt.target" {
			return true, "kube stop (halt.target queued)"
		}
	}
	return false, "no halt.target job and no suspend marker"
}

// Run saves the session if warranted. It only logs errors.
func Run(stateDir string) {
	save, why := Decide(stateDir, sysd.ListJobs)
	if !save {
		log.Printf("save-session: not saving: %s", why)
		return
	}
	log.Printf("save-session: saving (%s)", why)
	if err := saveSession(); err != nil {
		log.Printf("save-session: %v", err)
		return
	}
	log.Print("save-session: saved")
}

func saveSession() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("session bus: %w", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	return conn.Object(ksmService, ksmPath).CallWithContext(ctx, saveMethod, 0, sessionName).Err
}
