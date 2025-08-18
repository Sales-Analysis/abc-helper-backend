// Package httpapi exposes HTTP routing, handlers, and small middleware helpers.
package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/Sales-Analysis/abc-helper-backend/internal/version"

	_ "github.com/Sales-Analysis/abc-helper-backend/docs" // swagger docs are registered via blank import
	httpSwagger "github.com/swaggo/http-swagger"
)

var buildInfo version.Info

// Router настраивает HTTP маршруты.
func Router(v version.Info) *http.ServeMux {
	buildInfo = v

	mux := http.NewServeMux()
	mux.HandleFunc("/", helloHandler)
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/ready", readyHandler)
	mux.HandleFunc("/version", versionHandler)

	// Swagger UI
	mux.Handle("/swagger/", httpSwagger.WrapHandler)
	return mux
}

// helloHandler
// @Summary      Hello world
// @Description  Returns hello world message
// @Tags         root
// @Produce      json
// @Success      200 {object} map[string]interface{}
// @Router       / [get]
func helloHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"message": "hello world",
		"time":    time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// healthzHandler
// @Summary      Liveness probe
// @Tags         health
// @Produce      json
// @Success      200 {object} map[string]string
// @Router       /healthz [get]
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyHandler
// @Summary      Readiness probe
// @Tags         health
// @Produce      json
// @Success      200 {object} map[string]string
// @Router       /ready [get]
func readyHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"ready": "true"})
}

// versionHandler
// @Summary      Build information
// @Tags         meta
// @Produce      json
// @Success      200 {object} version.Info
// @Router       /version [get]
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, buildInfo)
}

// nolint:unparam // статус 200 используется сейчас во всех ручках; оставляем аргумент для будущих non-200 ответов
// jsonResponse — унифицированный JSON-ответ + простой access-лог.
func jsonResponse(w http.ResponseWriter, status int, payload any) {
	start := time.Now()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
	log.Printf("%d %v", status, time.Since(start))
}
