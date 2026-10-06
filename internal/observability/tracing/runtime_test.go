package tracing

import (
	"bytes"
	"context"
	"errors"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTraceContextAndLogCorrelation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	var output bytes.Buffer
	logger := slog.New(LogHandler{slog.NewJSONHandler(&output, nil)})
	runtime := &Runtime{Provider: provider, Logger: logger}
	parent := "00-11111111111111111111111111111111-2222222222222222-01"
	ctx := Extract(context.Background(), map[string]string{"traceparent": parent, "baggage": "secret=value"})
	ctx, span := runtime.Start(ctx, "financial.transaction", trace.SpanKindInternal)
	logger.InfoContext(ctx, "completed")
	carrier := Carrier(ctx)
	if len(carrier) != 1 || !strings.Contains(carrier["traceparent"], "11111111111111111111111111111111") {
		t.Fatal("trace context not preserved or baggage propagated")
	}
	runtime.End(ctx, span, "financial.transaction", nil)
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Parent().SpanID().String() != "2222222222222222" {
		t.Fatal("parent span lost")
	}
	if !strings.Contains(output.String(), `"trace_id":"11111111111111111111111111111111"`) || strings.Contains(output.String(), "secret") {
		t.Fatal("incorrect structured log context")
	}
}
func TestInvalidTraceParentIsIgnored(t *testing.T) {
	ctx := Extract(context.Background(), map[string]string{"traceparent": "00-invalid-secret", "tracestate": strings.Repeat("x", 513)})
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("invalid parent accepted")
	}
	if len(Carrier(ctx)) != 0 {
		t.Fatal("invalid context propagated")
	}
}

type blockedExporter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *blockedExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return errors.New("collector unavailable")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *blockedExporter) Shutdown(context.Context) error { return nil }
func TestBlockedExporterDoesNotBlockApplicationSpanCompletion(t *testing.T) {
	exporter := &blockedExporter{started: make(chan struct{}), release: make(chan struct{})}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Millisecond), sdktrace.WithMaxQueueSize(8), sdktrace.WithExportTimeout(2*time.Second)))
	defer func() {
		close(exporter.release)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	}()
	runtime := &Runtime{Provider: provider}
	ctx, span := runtime.Start(context.Background(), "first", trace.SpanKindInternal)
	runtime.End(ctx, span, "first", nil)
	select {
	case <-exporter.started:
	case <-time.After(time.Second):
		t.Fatal("export did not start")
	}
	completed := make(chan struct{})
	go func() {
		ctx, end := runtime.Begin(context.Background(), "financial.transaction")
		_ = ctx
		end(nil)
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("application span blocked on exporter")
	}
}
