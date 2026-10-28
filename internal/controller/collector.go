// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

// Collector exports the per-desktop gauges (docs/contract.md, section 11) from the cache.
// It emits nothing unless this replica is the leader, so series are never duplicated.
type Collector struct {
	Reader client.Reader
	// Elected is closed once this replica leads.
	Elected   <-chan struct{}
	Namespace string
}

var (
	desktopLabel = []string{"desktop"}

	descInfo     = prometheus.NewDesc("webtop_desktop_info", "Desktop phase and stage (always 1).", []string{"desktop", "user", "phase", "stage"}, nil)
	descPowerOn  = prometheus.NewDesc("webtop_desktop_power_on", "1 when powerState is On.", desktopLabel, nil)
	descRunning  = prometheus.NewDesc("webtop_desktop_running", "1 when phase is Running.", desktopLabel, nil)
	descAuth     = prometheus.NewDesc("webtop_desktop_authorized", "1 when the user is authorized.", desktopLabel, nil)
	descPending  = prometheus.NewDesc("webtop_desktop_update_pending", "1 when a pod template update is pending.", desktopLabel, nil)
	descProgress = prometheus.NewDesc("webtop_desktop_boot_progress", "Boot progress 0-100.", desktopLabel, nil)
	descClients  = prometheus.NewDesc("webtop_desktop_connected_clients", "Clients connected to the desktop.", desktopLabel, nil)
	descIdle     = prometheus.NewDesc("webtop_desktop_idle_seconds", "Idle seconds reported by the agent.", desktopLabel, nil)
	descLastStop = prometheus.NewDesc("webtop_desktop_last_stop_timestamp_seconds", "Unix time of the last recorded stop.", []string{"desktop", "reason"}, nil)
	allDescs     = []*prometheus.Desc{descInfo, descPowerOn, descRunning, descAuth, descPending, descProgress, descClients, descIdle, descLastStop}
)

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range allDescs {
		ch <- d
	}
}

func (c *Collector) leader() bool {
	select {
	case <-c.Elected:
		return true
	default:
		return false
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	if !c.leader() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var list v1alpha1.DesktopList
	if err := c.Reader.List(ctx, &list, client.InNamespace(c.Namespace)); err != nil {
		slog.Warn("collect desktop metrics", "err", err)
		return
	}
	g := func(d *prometheus.Desc, v float64, lv ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, lv...)
	}
	for i := range list.Items {
		d := &list.Items[i]
		st := &d.Status
		auth := false
		for _, cond := range st.Conditions {
			if cond.Type == v1alpha1.ConditionAuthorized {
				auth = cond.Status == metav1.ConditionTrue
			}
		}
		g(descInfo, 1, d.Name, d.Spec.User, string(st.Phase), metricStage(st.Stage))
		g(descPowerOn, b2f(d.Spec.PowerState == v1alpha1.PowerOn), d.Name)
		g(descRunning, b2f(st.Phase == v1alpha1.PhaseRunning), d.Name)
		g(descAuth, b2f(auth), d.Name)
		g(descPending, b2f(st.UpdatePending), d.Name)
		g(descProgress, float64(st.BootProgress), d.Name)
		g(descClients, float64(st.ConnectedClients), d.Name)
		g(descIdle, float64(st.IdleSeconds), d.Name)
		if st.LastStop != nil {
			g(descLastStop, float64(st.LastStop.Time.UnixMicro())/1e6, d.Name, string(st.LastStop.Reason))
		}
	}
}
