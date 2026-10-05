package config

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}

	if cfg.HTTPAddress != "127.0.0.1:8080" {
		t.Errorf("unexpected HTTP address: %s", cfg.HTTPAddress)
	}

	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("unexpected log level: %s", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	values := map[string]string{
		"HTTP_ADDR": "0.0.0.0:9090",
		"LOG_LEVEL": "debug",
	}

	cfg, err := load(func(key string) (string, bool) {
		value, exists := values[key]
		return value, exists
	})
	if err != nil {
		t.Fatalf("load overrides: %v", err)
	}

	if cfg.HTTPAddress != "0.0.0.0:9090" {
		t.Errorf("unexpected HTTP address: %s", cfg.HTTPAddress)
	}

	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("unexpected log level: %s", cfg.LogLevel)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"empty address", "HTTP_ADDR", ""},
		{"missing port", "HTTP_ADDR", "localhost"},
		{"invalid port", "HTTP_ADDR", "localhost:abc"},
		{"zero port", "HTTP_ADDR", "localhost:0"},
		{"negative port", "HTTP_ADDR", "localhost:-1"},
		{"port above limit", "HTTP_ADDR", "localhost:65536"},
		{"host whitespace", "HTTP_ADDR", "local host:8080"},
		{"empty log level", "LOG_LEVEL", ""},
		{"unknown log level", "LOG_LEVEL", "TRACE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(func(key string) (string, bool) {
				if key == tt.key {
					return tt.value, true
				}
				return "", false
			})

			if err == nil {
				t.Fatal("expected a configuration error")
			}

			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error must identify %s: %v", tt.key, err)
			}
		})
	}
}
