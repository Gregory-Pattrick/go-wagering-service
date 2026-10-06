package workers

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"go.uber.org/fx"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeQueue struct {
	message                      delivery.Message
	deleted, released, committed bool
	deleteErr                    error
	delay                        time.Duration
}

func (q *fakeQueue) Receive(ctx context.Context) (*delivery.Message, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return &q.message, nil
}
func (q *fakeQueue) Delete(context.Context, delivery.Message) error {
	if !q.committed {
		return errors.New("delete before commit")
	}
	q.deleted = true
	return q.deleteErr
}
func (q *fakeQueue) Release(_ context.Context, _ delivery.Message, delay time.Duration) error {
	q.released = true
	q.delay = delay
	return nil
}

type handlerFunc func(context.Context, delivery.Message) error

func (f handlerFunc) Handle(ctx context.Context, m delivery.Message) error { return f(ctx, m) }
func TestConsumerAcknowledgmentBoundary(t *testing.T) {
	for _, mode := range []string{"success", "business-rejection", "handler-error", "delete-error"} {
		t.Run(mode, func(t *testing.T) {
			q := &fakeQueue{message: delivery.Message{ReceiveCount: 1}}
			injected := errors.New("injected")
			if mode == "delete-error" {
				q.deleteErr = injected
			}
			handler := handlerFunc(func(context.Context, delivery.Message) error {
				if mode == "handler-error" {
					return injected
				}
				q.committed = true
				return nil
			})
			c := NewConsumer(q, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
			err := c.One(context.Background())
			switch mode {
			case "success", "business-rejection":
				if err != nil || !q.deleted || q.released {
					t.Fatal("committed outcome not acknowledged", err)
				}
			case "handler-error":
				if !errors.Is(err, injected) || q.deleted || !q.released {
					t.Fatal("failed processing was deleted")
				}
			case "delete-error":
				if !errors.Is(err, injected) || !q.committed || !q.released {
					t.Fatal("ack failure did not retain delivery")
				}
			}
		})
	}
}
func TestConsumerShutdownCancelsAndReleasesReceipt(t *testing.T) {
	entered := make(chan struct{})
	q := &fakeQueue{message: delivery.Message{ReceiveCount: 1}}
	handler := handlerFunc(func(ctx context.Context, _ delivery.Message) error { close(entered); <-ctx.Done(); return ctx.Err() })
	consumer := NewConsumer(q, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	app := fx.New(fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) { RegisterConsumer(lc, consumer) }))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("consumer did not start")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if q.deleted || !q.released || q.delay != 0 {
		t.Fatal("shutdown did not release uncommitted receipt")
	}
}
