// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	DBMaxConns      int32
	DBMinConns      int32
	// APITokens, when non-empty, are the bearer tokens accepted on /v1 routes.
	// Empty leaves the /v1 surface open (front it with a gateway).
	APITokens []string
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
		DBMaxConns:      10,
		DBMinConns:      0,
		APITokens:       splitAndTrim(os.Getenv("LEDGER_API_TOKENS")),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	var err error
	if c.ShutdownTimeout, err = duration("SHUTDOWN_TIMEOUT", c.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if c.RequestTimeout, err = duration("REQUEST_TIMEOUT", c.RequestTimeout); err != nil {
		return Config{}, err
	}
	if c.DBMaxConns, err = int32Env("DB_MAX_CONNS", c.DBMaxConns); err != nil {
		return Config{}, err
	}
	if c.DBMinConns, err = int32Env("DB_MIN_CONNS", c.DBMinConns); err != nil {
		return Config{}, err
	}
	if c.DBMinConns > c.DBMaxConns {
		return Config{}, fmt.Errorf("DB_MIN_CONNS (%d) exceeds DB_MAX_CONNS (%d)", c.DBMinConns, c.DBMaxConns)
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitAndTrim(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
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

func int32Env(key string, fallback int32) (int32, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: must be a non-negative integer", key)
	}
	return int32(n), nil
}
