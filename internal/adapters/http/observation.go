package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
)

// Route labels are an explicit allowlist, never a raw URL, UUID or provider ID.
func boundedRoute(r *http.Request) string {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) == 1 && p[0] == "wallets" {
		return "wallets"
	}
	if len(p) == 2 && p[0] == "wallets" {
		return "wallet"
	}
	if len(p) == 3 && p[0] == "wallets" && (p[2] == "ledger" || p[2] == "reconciliation") {
		return "wallet_" + p[2]
	}
	if len(p) >= 2 && len(p) <= 3 && p[0] == "wagering" && p[1] == "transactions" {
		return "transactions"
	}
	if len(p) == 5 && p[0] == "providers" && p[2] == "wagering" && p[3] == "transactions" {
		return "external_transaction"
	}
	if r.URL.Path == "/metrics" {
		return "metrics"
	}
	if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
		return "health"
	}
	return "unmatched"
}

type observedWriter struct {
	http.ResponseWriter
	status int
}

func (w *observedWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *observedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func ObserveServer(server *http.Server, authentication *Authentication, h *observability.Health, registry *observability.Registry, logger *slog.Logger) *http.Server {
	original := server.Handler
	protectedMetrics := authentication.Protect(RequireInternal(registry.Handler()))
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &observedWriter{ResponseWriter: w}
		switch {
		case (r.Method == "GET" || r.Method == "HEAD") && r.URL.Path == "/health/ready":
			h.Ready(wrapped, r)
		case (r.Method == "GET" || r.Method == "HEAD") && r.URL.Path == "/metrics":
			protectedMetrics.ServeHTTP(wrapped, r)
		default:
			original.ServeHTTP(wrapped, r)
		}
		status := wrapped.status
		if status == 0 {
			status = 200
		}
		class := "other"
		switch status / 100 {
		case 2:
			class = "2xx"
		case 3:
			class = "3xx"
		case 4:
			class = "4xx"
		case 5:
			class = "5xx"
		}
		route := boundedRoute(r)
		registry.HTTP.WithLabelValues(route, class).Inc()
		registry.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
		if route != "health" && route != "metrics" {
			logger.InfoContext(r.Context(), "HTTP request completed", "route", route, "status", status, "correlation_id", wrapped.Header().Get("X-Correlation-ID"), "duration_ms", time.Since(start).Milliseconds())
		}
	})
	return server
}
