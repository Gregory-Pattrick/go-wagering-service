package observability

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type toggleQueue struct{ down atomic.Bool }

func (q *toggleQueue) Inspect(context.Context, string) (QueueDepth, error) {
	if q.down.Load() {
		return QueueDepth{}, errors.New("sensitive broker URL")
	}
	return QueueDepth{Visible: 3}, nil
}
func TestReadinessFailureRecoveryAndStaleSample(t *testing.T) {
	var dbDown atomic.Bool
	q := &toggleQueue{}
	registry := NewRegistry()
	db := func(context.Context) (map[string]float64, error) {
		if dbDown.Load() {
			return nil, errors.New("sensitive DSN")
		}
		return map[string]float64{"outbox_pending": 4}, nil
	}
	h := NewHealth(config.TelemetryConfig{Interval: time.Second, Queues: map[string]string{"input": "queue"}, CollectDatabaseStats: true}, db, q, registry)
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.Ready(w, httptest.NewRequest("GET", "/health/ready", nil))
		if w.Code != want {
			t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "sensitive") {
			t.Fatal("probe leaked credentials")
		}
	}
	check(503)
	h.Check(context.Background())
	check(200)
	q.down.Store(true)
	h.Check(context.Background())
	check(503)
	q.down.Store(false)
	dbDown.Store(true)
	h.Check(context.Background())
	check(503)
	metrics := httptest.NewRecorder()
	registry.Handler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `wagering_database_state{measure="outbox_pending"} NaN`) {
		t.Fatal("failed database probe retained a misleading zero/value")
	}
	dbDown.Store(false)
	h.Check(context.Background())
	check(200)
	h.mu.Lock()
	h.checked = time.Now().Add(-time.Minute)
	h.mu.Unlock()
	check(503)
}
