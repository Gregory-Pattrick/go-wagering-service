package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type ConsumerConfig struct {
	Region, Endpoint, QueueURL, CredentialsFile string
	Providers                                   []string
}

func LoadConsumer() (ConsumerConfig, error) {
	c := ConsumerConfig{Region: os.Getenv("AWS_REGION"), Endpoint: os.Getenv("SQS_ENDPOINT"), QueueURL: os.Getenv("SQS_INPUT_QUEUE_URL"), CredentialsFile: os.Getenv("SQS_CONSUMER_CREDENTIALS_FILE")}
	if strings.TrimSpace(c.Region) == "" || strings.TrimSpace(c.CredentialsFile) == "" {
		return c, fmt.Errorf("AWS_REGION and SQS_CONSUMER_CREDENTIALS_FILE are required")
	}
	for _, raw := range []string{c.Endpoint, c.QueueURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("consumer SQS URLs must be HTTP(S) URLs without credentials, query or fragment")
		}
	}
	if !strings.HasSuffix(c.QueueURL, ".fifo") {
		return c, fmt.Errorf("SQS_INPUT_QUEUE_URL must name a FIFO queue")
	}
	raw, ok := os.LookupEnv("SQS_ALLOWED_PROVIDERS")
	if !ok {
		raw = "provider-a,provider-b"
	}
	seen := map[string]bool{}
	for _, provider := range strings.Split(raw, ",") {
		if provider == "" || strings.TrimSpace(provider) != provider || len(provider) > 256 || strings.ContainsAny(provider, "\t\r\n") || seen[provider] {
			return c, fmt.Errorf("SQS_ALLOWED_PROVIDERS must contain unique nonempty provider IDs")
		}
		seen[provider] = true
		c.Providers = append(c.Providers, provider)
	}
	return c, nil
}
