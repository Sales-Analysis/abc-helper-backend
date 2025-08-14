// Package main wires and runs the HTTP server for abc-helper-backend.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sales-Analysis/abc-helper-backend/internal/config"
	"github.com/Sales-Analysis/abc-helper-backend/internal/httpapi"
	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
)

func main() {
	cfg := config.Load()

	mux := httpapi.Router(version.Get()) // /, /healthz, /ready, /version

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	// start
	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	log.Printf("shutting down, timeout=%s", cfg.ShutdownTimeout)
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		_ = srv.Close()
	}
	log.Println("bye")
}
