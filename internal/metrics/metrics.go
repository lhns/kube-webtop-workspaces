// SPDX-License-Identifier: Apache-2.0

// Package metrics defines the controller-side Prometheus metrics (docs/contract.md, section 11)
// that more than one package updates. They live on controller-runtime's registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	StopsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "webtop_desktop_stops_total", Help: "Recorded desktop stops by reason.",
	}, []string{"reason"})

	StartDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "webtop_desktop_start_duration_seconds", Help: "Time from powerState On to phase Running.",
		Buckets: []float64{5, 10, 20, 30, 45, 60, 90, 120, 180, 300, 600},
	})

	PowerRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "webtop_power_requests_total", Help: "Power API requests.",
	}, []string{"action", "reason", "code"})

	ReportRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "webtop_report_requests_total", Help: "Report API requests.",
	}, []string{"code"})

	LDAPSyncs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "webtop_ldap_sync_total", Help: "LDAP syncs by result.",
	}, []string{"result"})

	LDAPLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "webtop_ldap_last_success_timestamp_seconds", Help: "Unix time of the last successful LDAP sync.",
	})

	LDAPMembers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "webtop_ldap_members", Help: "Members of the LDAP group at the last successful sync.",
	})
)

func init() {
	crmetrics.Registry.MustRegister(StopsTotal, StartDuration, PowerRequests, ReportRequests,
		LDAPSyncs, LDAPLastSuccess, LDAPMembers)
}
