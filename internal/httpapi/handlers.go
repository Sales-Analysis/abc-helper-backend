package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
)

var buildInfo version.Info

// @Summary Hello world
// @Tags    root
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router  / [get]
func helloHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"message": "hello world",
		"time":    time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// @Summary Liveness
// @Tags    health
// @Produce json
// @Success 200 {object} map[string]string
// @Router  /healthz [get]
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// @Summary Readiness
// @Tags    health
// @Produce json
// @Success 200 {object} map[string]string
// @Router  /ready [get]
func readyHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"ready": "true"})
}

// @Summary Build info
// @Tags    meta
// @Produce json
// @Success 200 {object} version.Info
// @Router  /version [get]
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, buildInfo)
}

// nolint:unparam
func jsonResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
