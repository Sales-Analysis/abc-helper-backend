package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

func Load() Config {
	return Config{
		Port:            getenv("PORT", "8080"),
		ReadTimeout:     dur("READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    dur("WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:     dur("IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: dur("SHUTDOWN_TIMEOUT", 10*time.Second),
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return def
}
