package httpapi

import (
	"log/slog"
	"net/http"

	_ "github.com/Sales-Analysis/abc-helper-backend/docs" // swagger docs are registered via blank import
	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
	httpSwagger "github.com/swaggo/http-swagger"
)

// Router собирает mux и возвращает уже обёрнутый middleware-ами http.Handler.
func Router(v version.Info, log *slog.Logger) http.Handler {
	buildInfo = v
	logger = log

	mux := http.NewServeMux()
	mux.HandleFunc("/", helloHandler)
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/ready", readyHandler)
	mux.HandleFunc("/version", versionHandler)

	mux.HandleFunc("/api/v1/abc/upload", abcUploadHandler)

	// Swagger UI
	mux.Handle("/swagger/", httpSwagger.WrapHandler)

	// /metrics
	mux.Handle("/metrics", metricsHandler())

	// Оборачиваем общий mux
	return withRequestID(withAccessLog(withMetrics(mux)))
}
