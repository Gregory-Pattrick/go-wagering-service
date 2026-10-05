package config

import (
	"strings"
	"testing"
)

func TestLoadDatabase(t *testing.T) {
	value := "postgres://wagering_app:local_password@localhost:5432/wagering?sslmode=disable"
	t.Setenv("DATABASE_URL", value)

	cfg, err := LoadDatabase()
	if err != nil {
		t.Fatalf("load database configuration: %v", err)
	}

	if cfg.URL != value {
		t.Fatal("database URL was unexpectedly modified")
	}
}

func TestLoadDatabaseRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"invalid URL", "postgres://user:secret@localhost/%zz"},
		{"unsupported scheme", "https://user:secret@localhost/wagering"},
		{"missing host", "postgres://user:secret@/wagering"},
		{"missing database", "postgres://user:secret@localhost"},
		{"missing username", "postgres://localhost/wagering"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tt.value)

			_, err := LoadDatabase()
			if err == nil {
				t.Fatal("expected a configuration error")
			}

			if strings.Contains(err.Error(), "secret") {
				t.Fatal("configuration error exposed a password")
			}
		})
	}
}
