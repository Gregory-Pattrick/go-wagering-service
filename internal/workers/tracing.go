package workers

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"time"
)

func restoreTrace(ctx context.Context, reader tracing.Reader, id string, reference bool) context.Context {
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var carrier map[string]string
	var err error
	if reference {
		carrier, err = reader.ReferenceCarrier(bounded, id)
	} else {
		carrier, err = reader.EventCarrier(bounded, id)
	}
	if err != nil {
		return ctx
	} // Missing telemetry must not prevent durable delivery.
	return tracing.Extract(ctx, carrier)
}

type tracedWorkerBackend struct {
	Backend
	runtime *tracing.Runtime
	reader  tracing.Reader
}

func (b tracedWorkerBackend) ClaimEvent(ctx context.Context, owner string, lease time.Duration) (delivery.Event, error) {
	ctx, end := b.runtime.Begin(ctx, "postgres.outbox_claim")
	e, err := b.Backend.ClaimEvent(ctx, owner, lease)
	if errors.Is(err, pgx.ErrNoRows) {
		end(nil)
	} else {
		end(err)
	}
	return e, err
}
func (b tracedWorkerBackend) ConfirmEvent(ctx context.Context, e delivery.Event) error {
	ctx = restoreTrace(ctx, b.reader, e.ID, false)
	ctx, end := b.runtime.Begin(ctx, "postgres.outbox_confirm")
	err := b.Backend.ConfirmEvent(ctx, e)
	end(err)
	return err
}
func (b tracedWorkerBackend) RetryEvent(ctx context.Context, e delivery.Event, d time.Duration) error {
	ctx = restoreTrace(ctx, b.reader, e.ID, false)
	ctx, end := b.runtime.Begin(ctx, "postgres.outbox_retry")
	err := b.Backend.RetryEvent(ctx, e, d)
	end(err)
	return err
}
func (b tracedWorkerBackend) ResumeReference(ctx context.Context, w delivery.ReferenceWork, delay, ttl time.Duration) (string, error) {
	ctx = restoreTrace(ctx, b.reader, w.ID, true)
	ctx, span := b.runtime.Start(ctx, "reference.resume", trace.SpanKindInternal)
	span.SetAttributes(attribute.Int("retry.attempt", w.Attempts))
	sql, end := b.runtime.Begin(ctx, "postgres.reference_transaction")
	status, err := b.Backend.ResumeReference(sql, w, delay, ttl)
	end(err)
	b.runtime.End(ctx, span, "reference.resume", err)
	return status, err
}

type tracedSender struct {
	delivery.Sender
	runtime *tracing.Runtime
	reader  tracing.Reader
}

func (s tracedSender) Send(ctx context.Context, e delivery.Event) error {
	ctx = restoreTrace(ctx, s.reader, e.ID, false)
	ctx, span := s.runtime.Start(ctx, "outbox.publish", trace.SpanKindProducer)
	span.SetAttributes(attribute.String("messaging.system", "aws_sqs"), attribute.String("messaging.message.id", e.ID), attribute.Int("retry.attempt", e.Attempts))
	err := s.Sender.Send(ctx, e)
	s.runtime.End(ctx, span, "outbox.publish", err)
	return err
}
func TraceRunner(r *Runner, runtime *tracing.Runtime, reader tracing.Reader) *Runner {
	return New(func() (Backend, error) {
		b, e := r.source()
		if e != nil {
			return nil, e
		}
		return tracedWorkerBackend{b, runtime, reader}, nil
	}, tracedSender{r.sender, runtime, reader}, r.cfg, r.logger)
}

type tracedHandler struct {
	delivery.Handler
	runtime *tracing.Runtime
}

func (h tracedHandler) Handle(ctx context.Context, m delivery.Message) error {
	ctx = tracing.Extract(ctx, m.TraceContext)
	ctx, span := h.runtime.Start(ctx, "sqs.process", trace.SpanKindConsumer)
	span.SetAttributes(attribute.String("messaging.system", "aws_sqs"), attribute.String("messaging.message.id", m.DeliveryID), attribute.Int("retry.attempt", m.ReceiveCount))
	err := h.Handler.Handle(ctx, m)
	h.runtime.End(ctx, span, "sqs.process", err)
	return err
}

type tracedQueue struct {
	delivery.Queue
	runtime *tracing.Runtime
}

func (q tracedQueue) Delete(ctx context.Context, m delivery.Message) error {
	ctx = tracing.Extract(ctx, m.TraceContext)
	ctx, span := q.runtime.Start(ctx, "sqs.delete", trace.SpanKindClient)
	err := q.Queue.Delete(ctx, m)
	q.runtime.End(ctx, span, "sqs.delete", err)
	return err
}
func (q tracedQueue) Release(ctx context.Context, m delivery.Message, d time.Duration) error {
	ctx = tracing.Extract(ctx, m.TraceContext)
	ctx, span := q.runtime.Start(ctx, "sqs.release", trace.SpanKindClient)
	err := q.Queue.Release(ctx, m, d)
	q.runtime.End(ctx, span, "sqs.release", err)
	return err
}

// Keep the previously installed metrics adapter while tracing the same handler.
func (h measuredHandler) WithTracer(t financial.TracePort) delivery.Handler {
	next := h.Handler
	if provider, ok := next.(interface {
		WithTracer(financial.TracePort) delivery.Handler
	}); ok {
		next = provider.WithTracer(t)
	}
	return measuredHandler{next, h.r, h.logger}
}
func TraceConsumer(c *Consumer, r *tracing.Runtime) *Consumer {
	handler := c.handler
	if source, ok := handler.(interface {
		WithTracer(financial.TracePort) delivery.Handler
	}); ok {
		handler = source.WithTracer(r)
	}
	return NewConsumer(tracedQueue{c.queue, r}, tracedHandler{handler, r}, c.logger)
}
