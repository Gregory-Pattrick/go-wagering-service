package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
)

type measuredBackend struct {
	Backend
	r *observability.Registry
}

func (b measuredBackend) ResumeReference(ctx context.Context, w delivery.ReferenceWork, delay, ttl time.Duration) (string, error) {
	start := time.Now()
	defer b.r.Elapsed("reference", start)
	status, err := b.Backend.ResumeReference(ctx, w, delay, ttl)
	if err != nil {
		b.r.Failures.WithLabelValues("reference", "unavailable").Inc()
	} else {
		b.r.Steps.WithLabelValues("reference_completed").Inc()
		if status == "PENDING_REFERENCE" || status == "PENDING" {
			b.r.Retries.WithLabelValues("reference_wait").Inc()
		} else {
			b.r.FinancialCompleted("reference", status, false)
		}
	}
	return status, err
}
func (b measuredBackend) ConfirmEvent(ctx context.Context, e delivery.Event) error {
	err := b.Backend.ConfirmEvent(ctx, e)
	if err == nil {
		b.r.Steps.WithLabelValues("outbox_ack").Inc()
	} else {
		b.r.Failures.WithLabelValues("outbox_ack", "unavailable").Inc()
	}
	return err
}
func (b measuredBackend) RetryEvent(ctx context.Context, e delivery.Event, d time.Duration) error {
	err := b.Backend.RetryEvent(ctx, e, d)
	if err == nil {
		b.r.Retries.WithLabelValues("outbox").Inc()
	}
	return err
}
func (b measuredBackend) RetryReference(ctx context.Context, w delivery.ReferenceWork, d time.Duration) error {
	err := b.Backend.RetryReference(ctx, w, d)
	if err == nil {
		b.r.Retries.WithLabelValues("reference_error").Inc()
	}
	return err
}

type measuredSender struct {
	delivery.Sender
	r      *observability.Registry
	logger *slog.Logger
}

func (s measuredSender) Send(ctx context.Context, e delivery.Event) error {
	start := time.Now()
	defer s.r.Elapsed("sqs_send", start)
	err := s.Sender.Send(ctx, e)
	var header struct {
		Correlation string `json:"correlationId"`
		Causation   string `json:"causationId"`
	}
	if json.Unmarshal(e.Payload, &header) == nil && financial.Text(header.Correlation, 128) {
		outcome := "accepted"
		if err != nil {
			outcome = "retry"
		}
		s.logger.InfoContext(ctx, "outbox send attempt", "event_id", e.ID, "correlation_id", header.Correlation, "causation_id", header.Causation, "outcome", outcome)
	}
	if err != nil {
		s.r.Failures.WithLabelValues("sqs_send", "unavailable").Inc()
	}
	return err
}
func ObserveRunner(runner *Runner, r *observability.Registry) *Runner {
	return New(func() (Backend, error) {
		b, err := runner.source()
		if err != nil {
			return nil, err
		}
		return measuredBackend{b, r}, nil
	}, measuredSender{runner.sender, r, runner.logger}, runner.cfg, runner.logger)
}

type measuredQueue struct {
	delivery.Queue
	r *observability.Registry
}

func (q measuredQueue) Receive(ctx context.Context) (*delivery.Message, error) {
	m, err := q.Queue.Receive(ctx)
	if err != nil {
		q.r.Failures.WithLabelValues("sqs_receive", "unavailable").Inc()
	} else if m != nil {
		q.r.Steps.WithLabelValues("sqs_received").Inc()
		if m.ReceiveCount > 1 {
			q.r.Retries.WithLabelValues("sqs_redelivery").Inc()
		}
	}
	return m, err
}
func (q measuredQueue) Delete(ctx context.Context, m delivery.Message) error {
	err := q.Queue.Delete(ctx, m)
	if err == nil {
		q.r.Steps.WithLabelValues("sqs_deleted").Inc()
	} else {
		q.r.Failures.WithLabelValues("sqs_delete", "unavailable").Inc()
	}
	return err
}
func (q measuredQueue) Release(ctx context.Context, m delivery.Message, d time.Duration) error {
	err := q.Queue.Release(ctx, m, d)
	if err == nil {
		q.r.Retries.WithLabelValues("sqs_visibility").Inc()
	} else {
		q.r.Failures.WithLabelValues("sqs_visibility", "unavailable").Inc()
	}
	return err
}

type measuredHandler struct {
	delivery.Handler
	r      *observability.Registry
	logger *slog.Logger
}

func (h measuredHandler) Handle(ctx context.Context, m delivery.Message) error {
	start := time.Now()
	defer h.r.Elapsed("sqs_handle", start)
	err := h.Handler.Handle(ctx, m)
	var envelope struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(m.Body, &envelope) == nil && financial.Text(envelope.MessageID, 256) {
		outcome := "committed"
		if err != nil {
			outcome = "retained"
		}
		h.logger.InfoContext(ctx, "SQS envelope handled", "message_id", envelope.MessageID, "delivery_id", m.DeliveryID, "outcome", outcome)
	}
	if err != nil {
		h.r.Failures.WithLabelValues("sqs_handle", "not_committed").Inc()
	}
	return err
}
func ObserveConsumer(consumer *Consumer, r *observability.Registry) *Consumer {
	handler := consumer.handler
	if source, ok := handler.(interface {
		WithObserver(financial.Observer) delivery.Handler
	}); ok {
		handler = source.WithObserver(r)
	}
	return NewConsumer(measuredQueue{consumer.queue, r}, measuredHandler{handler, r, consumer.logger}, consumer.logger)
}
