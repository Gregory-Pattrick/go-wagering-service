package httpapi

import (
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"net/http"
)

func TraceServer(server *http.Server, runtime *tracing.Runtime) *http.Server {
	original := server.Handler
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := boundedRoute(r)
		if route == "health" || route == "metrics" {
			original.ServeHTTP(w, r)
			return
		}
		parent := tracing.Extract(r.Context(), map[string]string{"traceparent": r.Header.Get("traceparent"), "tracestate": r.Header.Get("tracestate")})
		method := r.Method
		switch method {
		case "GET", "POST", "HEAD":
		default:
			method = "OTHER"
		}
		name := "HTTP " + method + " " + route
		ctx, span := runtime.Start(parent, name, trace.SpanKindServer)
		defer span.End()
		span.SetAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route))
		w.Header().Set("X-Trace-ID", span.SpanContext().TraceID().String())
		wrapped := &observedWriter{ResponseWriter: w}
		original.ServeHTTP(wrapped, r.WithContext(ctx))
		status := wrapped.status
		if status == 0 {
			status = 200
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, "server_error")
		}
		if runtime.Logger != nil {
			runtime.Logger.InfoContext(ctx, "HTTP trace completed", "route", route, "status", status)
		}
	})
	return server
}
