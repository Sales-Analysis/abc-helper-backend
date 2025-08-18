// Package httpapi contains HTTP handlers for the service.
package httpapi

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	registry = prometheus.NewRegistry()
	inFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "http_in_flight_requests", Help: "In-flight HTTP requests."},
	)
	reqTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "http_requests_total", Help: "Total HTTP requests."},
		[]string{"path", "method", "code"},
	)
	reqDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "Duration.", Buckets: prometheus.DefBuckets},
		[]string{"path", "method"},
	)
	helloTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "app_hello_requests_total", Help: "Hello hits."},
	)
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		inFlight, reqTotal, reqDuration, helloTotal,
	)
}

func metricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func withMetrics(next http.Handler) http.Handler {
	return promhttp.InstrumentHandlerInFlight(inFlight,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, method := r.URL.Path, r.Method
			// duration by path+method
			obs := reqDuration.MustCurryWith(prometheus.Labels{"path": path, "method": method})
			h := promhttp.InstrumentHandlerDuration(obs, next)
			// counter by path+method+code
			cnt := reqTotal.MustCurryWith(prometheus.Labels{"path": path, "method": method})
			h = promhttp.InstrumentHandlerCounter(cnt, h)
			h.ServeHTTP(w, r)
		}),
	)
}
