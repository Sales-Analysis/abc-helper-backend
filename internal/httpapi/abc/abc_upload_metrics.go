// Package abc contains handlers, validation logic, errors and metrics for ABC analysis endpoints.
package abc

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// UploadCounter counts XLSX uploads grouped by status (success or different validation errors).
	UploadCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "abc_upload_total",
			Help: "Total number of XLSX upload attempts",
		},
		[]string{"status"}, // Возможные значения:
		// success, empty_file, invalid_extension,
		// invalid_xlsx, no_sheets, no_data,
		// missing_value, read_error, invalid_form, no_file
	)
	// UploadDuration measures the processing time of XLSX uploads.
	UploadDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "abc_upload_duration_seconds",
			Help:    "Time taken to process XLSX upload",
			Buckets: prometheus.DefBuckets,
		},
	)
)

func init() {
	prometheus.MustRegister(UploadCounter, UploadDuration)
}
