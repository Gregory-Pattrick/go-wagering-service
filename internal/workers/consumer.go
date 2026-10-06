package workers

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"go.uber.org/fx"
)

type Consumer struct {
	queue   delivery.Queue
	handler delivery.Handler
	logger  *slog.Logger
}

func NewConsumer(queue delivery.Queue, handler delivery.Handler, logger *slog.Logger) *Consumer {
	return &Consumer{queue: queue, handler: handler, logger: logger}
}
func (c *Consumer) One(ctx context.Context) error {
	receive, cancel := context.WithTimeout(ctx, 15*time.Second)
	message, err := c.queue.Receive(receive)
	cancel()
	if err != nil || message == nil {
		return err
	}
	operation, stop := context.WithTimeout(ctx, 10*time.Second)
	err = c.handler.Handle(operation, *message)
	if err == nil {
		err = c.queue.Delete(operation, *message)
	}
	stop()
	if err == nil {
		c.logger.InfoContext(ctx, "SQS delivery acknowledged", "delivery_id", message.DeliveryID)
		return nil
	}
	// A business rejection returns nil after its durable commit, and is deleted.
	// Invalid envelopes, identity conflicts and I/O failures are never deleted.
	// The broker redrive policy (maxReceiveCount=5) owns the eventual DLQ move.
	reason := "transient_or_ack_failure"
	var invalid *financial.Error
	if errors.As(err, &invalid) {
		reason = invalid.Code
	}
	c.logger.WarnContext(ctx, "SQS delivery retained", "delivery_id", message.DeliveryID, "receive_count", message.ReceiveCount, "reason", reason)
	delay := Backoff(message.ReceiveCount, time.Second, time.Minute)
	if ctx.Err() != nil {
		delay = 0
	}
	cleanup, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	releaseErr := c.queue.Release(cleanup, *message, delay)
	return errors.Join(err, releaseErr)
}
func RegisterConsumer(lifecycle fx.Lifecycle, c *Consumer) {
	var cancel context.CancelFunc
	var done chan struct{}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			done = make(chan struct{})
			go func() {
				defer close(done)
				for ctx.Err() == nil {
					err := c.One(ctx)
					if ctx.Err() != nil {
						return
					}
					if err != nil {
						// Receive failures have no receipt to release. Bound repeated broker errors.
						c.logger.WarnContext(ctx, "consumer attempt deferred")
						timer := time.NewTimer(time.Second)
						select {
						case <-ctx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
			}()
			c.logger.Info("consumer started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel == nil {
				return nil
			}
			cancel()
			select {
			case <-done:
				c.logger.Info("consumer stopped")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
