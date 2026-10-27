// SPDX-License-Identifier: Apache-2.0

package gateway

import "github.com/prometheus/client_golang/prometheus"

// Outcomes of webtop_gateway_requests_total.
const (
	outProxy       = "proxy"
	outBoot        = "boot"
	outOff         = "off"
	outStart       = "start"
	outUnavailable = "unavailable"
	outDenied      = "denied"
	outNotFound    = "notfound"
	outError       = "error"
)

// Start triggers of webtop_gateway_starts_total.
const (
	triggerCreate   = "create"
	triggerNavigate = "navigate"
	triggerButton   = "button"
)

// Metrics are the gateway's own series (docs/contract.md, section 11).
type Metrics struct {
	Requests   *prometheus.CounterVec
	Websockets prometheus.Gauge
	Starts     *prometheus.CounterVec
}

// NewMetrics registers the gateway metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "webtop_gateway_requests_total", Help: "Gateway requests by outcome.",
		}, []string{"outcome"}),
		Websockets: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "webtop_gateway_websockets", Help: "Open proxied websockets.",
		}),
		Starts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "webtop_gateway_starts_total", Help: "Desktop starts requested by the gateway.",
		}, []string{"trigger"}),
	}
	reg.MustRegister(m.Requests, m.Websockets, m.Starts)
	return m
}
