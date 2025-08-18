// Package logx provides structured logger initialization via slog.
package logx

import (
	"log/slog"
	"os"
	"strings"
)

// New returns configured slog.Logger; JSON to stdout, level via env.
func New(level string) *slog.Logger {
	lev := parseLevel(level)
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lev,
	})
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
