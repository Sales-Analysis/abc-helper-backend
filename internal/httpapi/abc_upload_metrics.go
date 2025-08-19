package httpapi

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	uploadCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "abc_upload_total",
			Help: "Total number of uploaded XLSX files",
		},
		[]string{"status"}, // success, invalid_format, empty_file, missing_value, etc.
	)

	uploadDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "abc_upload_duration_seconds",
			Help:    "Time taken to process XLSX upload",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	prometheus.MustRegister(uploadCounter, uploadDuration)
}
