// @title           ABC Helper Backend API
// @version         0.1.0
// @description     Minimal HTTP API for abc-helper-backend (hello, probes, version).

// @contact.name    ABC Helper Team
// @contact.url     https://github.com/Sales-Analysis/abc-helper-backend
// @contact.email   vladislavtagaev@gmail.com

// @host      localhost:8080
// @BasePath  /
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sales-Analysis/abc-helper-backend/internal/config"
	"github.com/Sales-Analysis/abc-helper-backend/internal/httpapi"
	"github.com/Sales-Analysis/abc-helper-backend/internal/version"
)

func main() {
	// Конфигурация
	cfg := config.Load()

	// Логгер (slog)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	// Маршрутизатор + middleware
	handler := httpapi.Router(version.Get(), log)

	// HTTP сервер
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	// Запуск сервера
	go func() {
		log.Info("server starting", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server failed", slog.Any("err", err))
			os.Exit(1)
		}
	}()

	// Graceful shutdown по сигналам
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Info("server shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("graceful shutdown failed", slog.Any("err", err))
	} else {
		log.Info("server exited cleanly")
	}
}
