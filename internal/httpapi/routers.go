package httpapi

import (
	"log/slog"
	"net/http"

	_ "github.com/Sales-Analysis/abc-helper-backend/docs" // swagger docs are registered via blank import
	"github.com/Sales-Analysis/abc-helper-backend/internal/config"
	"github.com/Sales-Analysis/abc-helper-backend/internal/httpapi/abc"
	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
	httpSwagger "github.com/swaggo/http-swagger"
)

// Router builds the HTTP mux, attaches middlewares and returns the HTTP handler.
// It wires system endpoints, business endpoints (e.g., ABC), Swagger UI and /metrics.
func Router(v version.Info, log *slog.Logger, cfg config.Config) http.Handler {
	buildInfo = v
	logger = log

	mux := http.NewServeMux()
	assistant := newAssistantProxy(cfg.AssistantBaseURL, cfg.AssistantTimeout)
	mux.HandleFunc("/", helloHandler)
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/ready", readyHandler)
	mux.HandleFunc("/version", versionHandler)

	// ABC upload
	mux.HandleFunc("/api/v1/abc/upload", abc.UploadHandler)
	mux.HandleFunc("/assistant/chat", assistant.HandleChat)
	mux.HandleFunc("/api/v1/assistant/chat", assistant.HandleChat)

	// Swagger UI
	mux.Handle("/swagger/", httpSwagger.WrapHandler)

	// /metrics
	mux.Handle("/metrics", metricsHandler())

	// Оборачиваем общий mux
	return withCORS(withRequestID(withAccessLog(withMetrics(mux))))
}
