package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPAddress string
	LogLevel    slog.Level
}

func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	address := valueOrDefault(lookup, "HTTP_ADDR", "127.0.0.1:8080")

	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must use host:port format")
	}

	if strings.TrimSpace(host) != host || strings.ContainsAny(host, " \t\r\n") {
		return Config{}, fmt.Errorf("HTTP_ADDR host must not contain whitespace")
	}

	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("HTTP_ADDR port must be between 1 and 65535")
	}

	levelText := strings.ToUpper(
		valueOrDefault(lookup, "LOG_LEVEL", "INFO"),
	)

	var level slog.Level

	switch levelText {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO":
		level = slog.LevelInfo
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		return Config{}, fmt.Errorf("LOG_LEVEL must be DEBUG, INFO, WARN or ERROR")
	}

	return Config{
		HTTPAddress: address,
		LogLevel:    level,
	}, nil
}

func valueOrDefault(
	lookup func(string) (string, bool),
	key string,
	fallback string,
) string {
	if value, exists := lookup(key); exists {
		return value
	}

	return fallback
}
