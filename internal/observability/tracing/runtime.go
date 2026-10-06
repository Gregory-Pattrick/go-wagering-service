// Package tracing implements bounded, asynchronous tracing outside the domain.
package tracing

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
)

type Runtime struct {
	Provider *sdktrace.TracerProvider
	Logger   *slog.Logger
}

func New(lc fx.Lifecycle, c config.TracingConfig, logger *slog.Logger, service string) (*Runtime, error) {
	exporter, err := otlptracehttp.New(context.Background(), otlptracehttp.WithEndpointURL(c.Endpoint), otlptracehttp.WithTimeout(2*time.Second), otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}))
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	processor := sdktrace.NewBatchSpanProcessor(exporter, sdktrace.WithMaxQueueSize(2048), sdktrace.WithMaxExportBatchSize(256), sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(2*time.Second))
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.SampleRatio))), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service), attribute.String("service.instance.id", host))))
	r := &Runtime{Provider: provider, Logger: logger}
	lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := provider.Shutdown(bounded); err != nil {
			logger.Warn("trace export incomplete during shutdown")
		}
		return nil // An unavailable collector cannot turn a completed financial operation into a failure.
	}})
	return r, nil
}
func (r *Runtime) Start(ctx context.Context, name string, kind trace.SpanKind) (context.Context, trace.Span) {
	return r.Provider.Tracer("go-wagering-service").Start(ctx, name, trace.WithSpanKind(kind))
}

// Begin implements the application tracing port without exposing SDK types there.
func (r *Runtime) Begin(ctx context.Context, name string) (context.Context, func(error)) {
	kind := trace.SpanKindInternal
	if strings.HasPrefix(name, "postgres.") {
		kind = trace.SpanKindClient
	}
	child, span := r.Start(ctx, name, kind)
	if kind == trace.SpanKindClient {
		span.SetAttributes(attribute.String("db.system.name", "postgresql"))
	}
	return child, func(err error) { r.End(child, span, name, err) }
}
func (r *Runtime) End(ctx context.Context, span trace.Span, name string, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "operation_failed")
		span.SetAttributes(attribute.String("error.type", "operation_failed"))
	}
	if r.Logger != nil {
		if name == "outbox.publish" || name == "sqs.process" || name == "reference.resume" {
			r.Logger.InfoContext(ctx, "trace operation completed", "operation", name, "failed", err != nil)
		} else {
			r.Logger.DebugContext(ctx, "trace operation completed", "operation", name, "failed", err != nil)
		}
	}
	span.End()
}
func Carrier(ctx context.Context) map[string]string {
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	return c
}
func Extract(ctx context.Context, carrier map[string]string) context.Context {
	// W3C trace context only: never accept or propagate arbitrary baggage.
	clean := propagation.MapCarrier{}
	if v := carrier["traceparent"]; len(v) <= 128 {
		clean["traceparent"] = v
	}
	if v := carrier["tracestate"]; len(v) <= 512 {
		clean["tracestate"] = v
	}
	return propagation.TraceContext{}.Extract(ctx, clean)
}

type LogHandler struct{ slog.Handler }

func (h LogHandler) Handle(ctx context.Context, r slog.Record) error {
	s := trace.SpanContextFromContext(ctx)
	if s.IsValid() {
		r.AddAttrs(slog.String("trace_id", s.TraceID().String()), slog.String("span_id", s.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
func (h LogHandler) WithAttrs(a []slog.Attr) slog.Handler { return LogHandler{h.Handler.WithAttrs(a)} }
func (h LogHandler) WithGroup(g string) slog.Handler      { return LogHandler{h.Handler.WithGroup(g)} }

// Reader fetches transport metadata separately from immutable business payloads.
type Reader interface {
	EventCarrier(context.Context, string) (map[string]string, error)
	ReferenceCarrier(context.Context, string) (map[string]string, error)
}
