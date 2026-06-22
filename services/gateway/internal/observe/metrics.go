// Package observe holds the gateway's Prometheus metrics and a small helper to
// capture response status for access logging and metrics labelling.
package observe

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics bundles the request counter and latency histogram, both labelled by the
// matched route (logical upstream or a synthetic label) and the response status.
type Metrics struct {
	reg      *prometheus.Registry
	Requests *prometheus.CounterVec
	Latency  *prometheus.HistogramVec
}

// NewMetrics registers the gateway's metrics on a dedicated registry so /metrics
// exposes exactly what we declare (plus Go runtime/process collectors).
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	m := &Metrics{
		reg: reg,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gateway",
			Name:      "requests_total",
			Help:      "Total HTTP requests handled by the gateway.",
		}, []string{"route", "method", "status"}),
		Latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gateway",
			Name:      "request_duration_seconds",
			Help:      "Request latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
	}
	reg.MustRegister(m.Requests, m.Latency)
	return m
}

// Handler returns the /metrics HTTP handler bound to this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
