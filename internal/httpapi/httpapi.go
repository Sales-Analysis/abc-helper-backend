// Package httpapi exposes HTTP routing, handlers, and small middleware helpers.
package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
)

// Router builds and returns the HTTP mux with public endpoints.
func Router(v version.Info) *http.ServeMux {
	mux := http.NewServeMux()

	// Hello World
	mux.HandleFunc("/", jsonHandler(func(_ *http.Request) (any, int, error) {
		return map[string]any{
			"message": "hello, world",
			"time":    time.Now().UTC().Format(time.RFC3339Nano),
		}, http.StatusOK, nil
	}))

	// Liveness
	mux.HandleFunc("/healthz", jsonHandler(func(_ *http.Request) (any, int, error) {
		return map[string]string{"status": "ok"}, http.StatusOK, nil
	}))

	// Readiness
	mux.HandleFunc("/ready", jsonHandler(func(_ *http.Request) (any, int, error) {
		return map[string]string{"ready": "true"}, http.StatusOK, nil
	}))

	// Build/Version info
	mux.HandleFunc("/version", jsonHandler(func(_ *http.Request) (any, int, error) {
		return v, http.StatusOK, nil
	}))

	return mux
}

// jsonHandler is a small helper that standardizes JSON responses and access logging.
func jsonHandler(fn func(r *http.Request) (any, int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		payload, code, err := fn(r)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, code)
		} else {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(payload)
		}

		log.Printf("%s %s %d %v", r.Method, r.URL.Path, code, time.Since(start))
	}
}
