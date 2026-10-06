package workers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"go.uber.org/fx"
)

type fakeBackend struct {
	event              delivery.Event
	confirmed, retried bool
	confirmErr         error
}

func (b *fakeBackend) ClaimEvent(context.Context, string, time.Duration) (delivery.Event, error) {
	return b.event, nil
}
func (b *fakeBackend) ConfirmEvent(context.Context, delivery.Event) error {
	b.confirmed = true
	return b.confirmErr
}
func (b *fakeBackend) RetryEvent(context.Context, delivery.Event, time.Duration) error {
	b.retried = true
	return nil
}
func (b *fakeBackend) ClaimReference(ctx context.Context, _ string, _ time.Duration) (delivery.ReferenceWork, error) {
	<-ctx.Done()
	return delivery.ReferenceWork{}, ctx.Err()
}
func (b *fakeBackend) ResumeReference(context.Context, delivery.ReferenceWork, time.Duration, time.Duration) (string, error) {
	return "", nil
}
func (b *fakeBackend) RetryReference(context.Context, delivery.ReferenceWork, time.Duration) error {
	return nil
}

type senderFunc func(context.Context, delivery.Event) error

func (f senderFunc) Send(ctx context.Context, e delivery.Event) error { return f(ctx, e) }
func testConfig() config.WorkersConfig {
	return config.WorkersConfig{Poll: time.Millisecond, Lease: 30 * time.Second, OperationTimeout: 10 * time.Second, RetryBase: time.Second, RetryCap: time.Minute, ReferenceTTL: 15 * time.Minute}
}
func TestPublishFailureDoesNotConfirmAndUnknownAckDoesNotMutateEvent(t *testing.T) {
	for _, mode := range []string{"send-error", "ack-error"} {
		t.Run(mode, func(t *testing.T) {
			b := &fakeBackend{event: delivery.Event{ID: "stable", Payload: []byte("snapshot"), Attempts: 1}}
			injected := errors.New("injected")
			if mode == "ack-error" {
				b.confirmErr = injected
			}
			sender := senderFunc(func(_ context.Context, e delivery.Event) error {
				if b.confirmed {
					t.Fatal("acknowledged before sending")
				}
				if e.ID != "stable" {
					t.Fatal("identity changed")
				}
				if mode == "send-error" {
					return injected
				}
				return nil
			})
			r := New(func() (Backend, error) { return b, nil }, sender, testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			found, err := r.PublishOne(context.Background())
			if !found || !errors.Is(err, injected) {
				t.Fatalf("%v %v", found, err)
			}
			if mode == "send-error" && (b.confirmed || !b.retried) {
				t.Fatal("failed send confirmed or not rescheduled")
			}
			if mode == "ack-error" && (!b.confirmed || b.retried) {
				t.Fatal("unknown ack must leave claim recoverable")
			}
		})
	}
}
func TestBackoffBounds(t *testing.T) {
	for attempt := 1; attempt < 100; attempt++ {
		for i := 0; i < 100; i++ {
			got := Backoff(attempt, time.Second, time.Minute)
			if got < time.Second || got > time.Minute {
				t.Fatalf("delay=%s", got)
			}
		}
	}
}
func TestFxStopsWorkersBeforeReturning(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	sender := senderFunc(func(ctx context.Context, _ delivery.Event) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	})
	b := &fakeBackend{event: delivery.Event{Attempts: 1}}
	r := New(func() (Backend, error) { return b, nil }, sender, testConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	app := fx.New(fx.NopLogger, fx.Invoke(func(lc fx.Lifecycle) { Register(lc, r) }))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker never started")
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	// Stop waits for both loops; the race detector checks their shared lifecycle.
}
