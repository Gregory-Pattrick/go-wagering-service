package workers

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/operationlog"
	"log/slog"
	"sync"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"
)

type Backend interface {
	delivery.Outbox
	delivery.References
}
type Source func() (Backend, error)
type Runner struct {
	source Source
	sender delivery.Sender
	cfg    config.WorkersConfig
	logger *slog.Logger
}

func New(source Source, sender delivery.Sender, cfg config.WorkersConfig, logger *slog.Logger) *Runner {
	return &Runner{source: source, sender: sender, cfg: cfg, logger: logger}
}

// PublishOne performs network I/O after the short claim transaction committed.
// A successful Send followed by a failed confirmation leaves a recoverable lease;
// a later delivery uses the exact same event ID and immutable payload.
func (r *Runner) PublishOne(ctx context.Context) (bool, error) {
	backend, err := r.source()
	if err != nil {
		return false, err
	}
	owner, err := financial.NewID()
	if err != nil {
		return false, err
	}
	event, err := backend.ClaimEvent(ctx, owner, r.cfg.Lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = r.sender.Send(ctx, event); err != nil {
		retryErr := backend.RetryEvent(ctx, event, Backoff(event.Attempts, r.cfg.RetryBase, r.cfg.RetryCap))
		return true, errors.Join(err, retryErr)
	}
	if err = backend.ConfirmEvent(ctx, event); err != nil {
		return true, err
	}
	operationlog.Published(ctx, r.logger, event.ID, event.Payload)
	r.logger.InfoContext(ctx, "outbox event published", "event_id", event.ID, "attempt", event.Attempts)
	return true, nil
}
func (r *Runner) ReferenceOne(ctx context.Context) (bool, error) {
	backend, err := r.source()
	if err != nil {
		return false, err
	}
	owner, err := financial.NewID()
	if err != nil {
		return false, err
	}
	work, err := backend.ClaimReference(ctx, owner, r.cfg.Lease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	delay := Backoff(work.Attempts, r.cfg.RetryBase, r.cfg.RetryCap)
	outcome, err := backend.ResumeReference(ctx, work, delay, r.cfg.ReferenceTTL)
	if err != nil {
		retryErr := backend.RetryReference(ctx, work, delay)
		return true, errors.Join(err, retryErr)
	}
	r.logger.InfoContext(ctx, "reference attempt completed", "transaction_id", work.ID, "status", outcome, "attempt", work.Attempts)
	return true, nil
}
func (r *Runner) loop(ctx context.Context, name string, step func(context.Context) (bool, error)) {
	for ctx.Err() == nil {
		operation, cancel := context.WithTimeout(ctx, r.cfg.OperationTimeout)
		found, err := step(operation)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// Do not log SDK request bodies, credentials or connection strings.
			reason := "operation_failed"
			if errors.Is(err, delivery.ErrLeaseLost) {
				reason = "lease_lost"
			}
			r.logger.ErrorContext(ctx, "worker attempt deferred", "worker", name, "reason", reason)
		}
		if !found || err != nil {
			timer := time.NewTimer(r.cfg.Poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
func Register(lifecycle fx.Lifecycle, r *Runner) {
	var cancel context.CancelFunc
	var done chan struct{}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if _, err := r.source(); err != nil {
				return err
			}
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			done = make(chan struct{})
			go func() {
				defer close(done)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); r.loop(ctx, "references", r.ReferenceOne) }()
				go func() { defer wg.Done(); r.loop(ctx, "outbox", r.PublishOne) }()
				wg.Wait()
			}()
			r.logger.Info("workers started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel == nil {
				return nil
			}
			cancel()
			select {
			case <-done:
				r.logger.Info("workers stopped")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
