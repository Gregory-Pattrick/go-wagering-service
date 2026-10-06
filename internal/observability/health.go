package observability

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"go.uber.org/fx"
)

type DatabaseProbe func(context.Context) (map[string]float64, error)
type QueueDepth struct{ Visible, Inflight, Delayed float64 }
type QueueProbe interface {
	Inspect(context.Context, string) (QueueDepth, error)
}
type Health struct {
	cfg          config.TelemetryConfig
	db           DatabaseProbe
	sqs          QueueProbe
	registry     *Registry
	mu           sync.RWMutex
	checked      time.Time
	dependencies map[string]bool
}

func NewHealth(c config.TelemetryConfig, db DatabaseProbe, sqs QueueProbe, r *Registry) *Health {
	return &Health{cfg: c, db: db, sqs: sqs, registry: r}
}
func (h *Health) Check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	dependencies := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	record := func(name string, up bool) {
		mu.Lock()
		dependencies[name] = up
		mu.Unlock()
		value := 0.
		if up {
			value = 1
		}
		h.registry.Dependencies.WithLabelValues(name).Set(value)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		facts, err := h.db(ctx)
		record("postgres", err == nil)
		if err != nil {
			if h.cfg.CollectDatabaseStats {
				for _, name := range DatabaseMeasures {
					h.registry.Facts.WithLabelValues(name).Set(math.NaN())
				}
			}
			return
		}
		for name, value := range facts {
			h.registry.Facts.WithLabelValues(name).Set(value)
		}
	}()
	for name, queue := range h.cfg.Queues {
		wg.Add(1)
		go func(name, queue string) {
			defer wg.Done()
			depth, err := h.sqs.Inspect(ctx, queue)
			record("sqs_"+name, err == nil)
			if err != nil {
				depth = QueueDepth{math.NaN(), math.NaN(), math.NaN()}
			}
			h.registry.Queues.WithLabelValues(name, "visible").Set(depth.Visible)
			h.registry.Queues.WithLabelValues(name, "inflight").Set(depth.Inflight)
			h.registry.Queues.WithLabelValues(name, "delayed").Set(depth.Delayed)
		}(name, queue)
	}
	wg.Wait()
	now := time.Now()
	h.mu.Lock()
	h.dependencies = dependencies
	h.checked = now
	h.mu.Unlock()
	h.registry.Sample.Set(float64(now.Unix()))
}
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ready := !h.checked.IsZero() && time.Since(h.checked) <= 2*h.cfg.Interval+3*time.Second
	for _, up := range h.dependencies {
		ready = ready && up
	}
	status := "ok"
	code := 200
	if !ready {
		status = "unavailable"
		code = 503
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "dependencies": h.dependencies, "checkedAt": h.checked.UTC()})
	}
}
func RegisterHealth(lifecycle fx.Lifecycle, h *Health) {
	var stop context.CancelFunc
	var done chan struct{}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			stop = cancel
			done = make(chan struct{})
			go func() {
				defer close(done)
				h.Check(ctx)
				ticker := time.NewTicker(h.cfg.Interval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						h.Check(ctx)
					}
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if stop == nil {
				return nil
			}
			stop()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}

var DatabaseMeasures = []string{"transactions_pending", "transactions_pending_reference", "transactions_processed", "transactions_rejected", "transactions_failed", "outbox_pending", "outbox_oldest_seconds", "outbox_retry_attempts", "references_pending", "references_oldest_seconds", "references_pending_attempts", "inbox_completed"}
