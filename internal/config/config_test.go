package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Run("requires DATABASE_URL", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		if _, err := Load(); err == nil {
			t.Fatal("expected error when DATABASE_URL is unset")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/db")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Addr != ":8080" || c.LogLevel != "info" || c.ShutdownTimeout != 15*time.Second {
			t.Fatalf("unexpected defaults: %+v", c)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/db")
		t.Setenv("LEDGER_ADDR", ":9000")
		t.Setenv("SHUTDOWN_TIMEOUT", "30s")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Addr != ":9000" || c.ShutdownTimeout != 30*time.Second {
			t.Fatalf("overrides not applied: %+v", c)
		}
	})

	t.Run("bad duration", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/db")
		t.Setenv("REQUEST_TIMEOUT", "not-a-duration")
		if _, err := Load(); err == nil {
			t.Fatal("expected parse error")
		}
	})
}
