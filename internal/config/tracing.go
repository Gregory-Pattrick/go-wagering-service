package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
)

type TracingConfig struct {
	Endpoint    string
	SampleRatio float64
}

func LoadTracing() (TracingConfig, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://otel-collector:4318/v1/traces"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/traces" {
		return TracingConfig{}, fmt.Errorf("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT must be an HTTP(S) URL ending in /v1/traces without credentials or query")
	}
	ratio := 1.0
	if raw, ok := os.LookupEnv("TRACING_SAMPLE_RATIO"); ok {
		ratio, err = strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			return TracingConfig{}, fmt.Errorf("TRACING_SAMPLE_RATIO must be between 0 and 1")
		}
	}
	return TracingConfig{Endpoint: endpoint, SampleRatio: ratio}, nil
}
