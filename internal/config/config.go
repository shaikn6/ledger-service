// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds all runtime configuration. Every field has an environment
// variable and a safe default except DatabaseURL, which is required.
type Config struct {
	DatabaseURL     string
	Addr            string
	LogLevel        string
	ShutdownTimeout time.Duration
	RequestTimeout  time.Duration
}

// Load reads configuration from the environment. It returns an error if a
// required variable is missing or a value cannot be parsed.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		Addr:            getenv("LEDGER_ADDR", ":8080"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
		ShutdownTimeout: 15 * time.Second,
		RequestTimeout:  10 * time.Second,
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if d, err := parseDuration("SHUTDOWN_TIMEOUT", c.ShutdownTimeout); err != nil {
		return Config{}, err
	} else {
		c.ShutdownTimeout = d
	}
	if d, err := parseDuration("REQUEST_TIMEOUT", c.RequestTimeout); err != nil {
		return Config{}, err
	} else {
		c.RequestTimeout = d
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
