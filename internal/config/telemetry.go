package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type TelemetryConfig struct {
	Address, Region, Endpoint, CredentialsFile string
	Queues                                     map[string]string
	Interval                                   time.Duration
	CollectDatabaseStats                       bool
}

func LoadTelemetry() (TelemetryConfig, error) {
	c := TelemetryConfig{Address: valueOrDefault(os.LookupEnv, "OPS_ADDR", "127.0.0.1:9090"), Region: os.Getenv("AWS_REGION"), Endpoint: os.Getenv("SQS_ENDPOINT"), CredentialsFile: os.Getenv("MONITOR_CREDENTIALS_FILE")}
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || strings.TrimSpace(host) != host {
		return c, fmt.Errorf("OPS_ADDR must use host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return c, fmt.Errorf("invalid OPS_ADDR port")
	}
	c.Interval, err = time.ParseDuration(valueOrDefault(os.LookupEnv, "PROBE_INTERVAL", "5s"))
	if err != nil || c.Interval < time.Second || c.Interval > time.Minute {
		return c, fmt.Errorf("PROBE_INTERVAL must be between 1s and 1m")
	}
	if c.Region == "" || c.CredentialsFile == "" {
		return c, fmt.Errorf("AWS_REGION and MONITOR_CREDENTIALS_FILE are required")
	}
	if err = json.Unmarshal([]byte(os.Getenv("MONITOR_QUEUES")), &c.Queues); err != nil || len(c.Queues) == 0 {
		return c, fmt.Errorf("MONITOR_QUEUES must be a nonempty JSON object")
	}
	allowed := map[string]bool{"input": true, "output": true, "input_dlq": true, "output_dlq": true}
	urls := []string{c.Endpoint}
	for name, raw := range c.Queues {
		if !allowed[name] {
			return c, fmt.Errorf("unsupported monitored queue alias")
		}
		urls = append(urls, raw)
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, fmt.Errorf("monitor URLs must use HTTP(S) without embedded credentials")
		}
	}
	return c, nil
}
