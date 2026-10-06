package config

import (
	"testing"
	"time"
)

func TestWorkerTimingValidation(t *testing.T) {
	defaults, err := loadWorkers(func(string) (string, bool) { return "", false })
	if err != nil || defaults.ReferenceTTL != 15*time.Minute || defaults.Lease != 30*time.Second {
		t.Fatalf("defaults: %+v %v", defaults, err)
	}
	for _, pair := range [][2]string{{"WORKER_LEASE", "10s"}, {"WORKER_RETRY_BASE", "61s"}, {"REFERENCE_TTL", "16m"}, {"WORKER_POLL_INTERVAL", "0s"}, {"WORKER_OPERATION_TIMEOUT", ""}} {
		_, err := loadWorkers(func(key string) (string, bool) {
			if key == pair[0] {
				return pair[1], true
			}
			return "", false
		})
		if err == nil {
			t.Fatalf("accepted invalid %s=%s", pair[0], pair[1])
		}
	}
}
