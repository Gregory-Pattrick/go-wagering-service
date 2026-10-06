package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type WorkersConfig struct {
	Poll, Lease, OperationTimeout, RetryBase, RetryCap, ReferenceTTL time.Duration
}

func LoadWorkers() (WorkersConfig, error) { return loadWorkers(os.LookupEnv) }
func loadWorkers(lookup func(string) (string, bool)) (WorkersConfig, error) {
	c := WorkersConfig{}
	entries := []struct {
		name, fallback string
		target         *time.Duration
	}{
		{"WORKER_POLL_INTERVAL", "500ms", &c.Poll},
		{"WORKER_LEASE", "30s", &c.Lease},
		{"WORKER_OPERATION_TIMEOUT", "10s", &c.OperationTimeout},
		{"WORKER_RETRY_BASE", "1s", &c.RetryBase},
		{"WORKER_RETRY_CAP", "60s", &c.RetryCap},
		{"REFERENCE_TTL", "15m", &c.ReferenceTTL},
	}
	for _, entry := range entries {
		value := valueOrDefault(lookup, entry.name, entry.fallback)
		d, err := time.ParseDuration(value)
		if err != nil || d < time.Millisecond || d > 24*time.Hour {
			return c, fmt.Errorf("%s must be between 1ms and 24h", entry.name)
		}
		*entry.target = d
	}
	if c.Lease < c.OperationTimeout*2 {
		return c, fmt.Errorf("WORKER_LEASE must be at least twice WORKER_OPERATION_TIMEOUT")
	}
	if c.RetryBase > c.RetryCap {
		return c, fmt.Errorf("WORKER_RETRY_BASE must not exceed WORKER_RETRY_CAP")
	}
	if c.ReferenceTTL > 15*time.Minute {
		return c, fmt.Errorf("REFERENCE_TTL cannot exceed the persisted 15m deadline")
	}
	return c, nil
}

type PublisherConfig struct{ Region, Endpoint, QueueURL, CredentialsFile string }

func LoadPublisher() (PublisherConfig, error) {
	c := PublisherConfig{Region: os.Getenv("AWS_REGION"), Endpoint: os.Getenv("SQS_ENDPOINT"), QueueURL: os.Getenv("SQS_EVENTS_QUEUE_URL"), CredentialsFile: os.Getenv("SQS_PUBLISHER_CREDENTIALS_FILE")}
	if strings.TrimSpace(c.Region) == "" || strings.TrimSpace(c.CredentialsFile) == "" {
		return c, fmt.Errorf("AWS_REGION and SQS_PUBLISHER_CREDENTIALS_FILE are required")
	}
	for _, raw := range []string{c.Endpoint, c.QueueURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return c, fmt.Errorf("SQS endpoints must be HTTP(S) URLs without credentials, query or fragment")
		}
	}
	if !strings.HasSuffix(c.QueueURL, ".fifo") {
		return c, fmt.Errorf("SQS_EVENTS_QUEUE_URL must name a FIFO queue")
	}
	return c, nil
}
