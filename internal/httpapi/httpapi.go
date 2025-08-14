package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
)

func Router(v version.Info) *http.ServeMux {
	mux := http.NewServeMux()

	// Hello World
	mux.HandleFunc("/", jsonHandler(func(r *http.Request) (any, int, error) {
		return map[string]any{
			"message": "hello, world",
			"time":    time.Now().UTC().Format(time.RFC3339Nano),
		}, http.StatusOK, nil
	}))

	// Liveness
	mux.HandleFunc("/healthz", jsonHandler(func(r *http.Request) (any, int, error) {
		return map[string]string{"status": "ok"}, http.StatusOK, nil
	}))

	// Readiness
	mux.HandleFunc("/ready", jsonHandler(func(r *http.Request) (any, int, error) {
		return map[string]string{"ready": "true"}, http.StatusOK, nil
	}))

	// Build/Version info
	mux.HandleFunc("/version", jsonHandler(func(r *http.Request) (any, int, error) {
		return v, http.StatusOK, nil
	}))

	return mux
}

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

		// простой access-лог: METHOD PATH CODE DURATION
		log.Printf("%s %s %d %v", r.Method, r.URL.Path, code, time.Since(start))
	}
}
