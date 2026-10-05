package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

type DatabaseConfig struct {
	URL string
}

func LoadDatabase() (DatabaseConfig, error) {
	value := os.Getenv("DATABASE_URL")
	if strings.TrimSpace(value) == "" {
		return DatabaseConfig{}, errors.New("DATABASE_URL is required")
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return DatabaseConfig{}, errors.New("DATABASE_URL must be a valid PostgreSQL URL")
	}

	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return DatabaseConfig{}, errors.New("DATABASE_URL must use postgres or postgresql")
	}

	if parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" {
		return DatabaseConfig{}, errors.New("DATABASE_URL must include a host and database")
	}

	if parsed.User == nil || parsed.User.Username() == "" {
		return DatabaseConfig{}, errors.New("DATABASE_URL must include a username")
	}

	return DatabaseConfig{URL: value}, nil
}
