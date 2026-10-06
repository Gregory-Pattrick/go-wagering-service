package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
)

type observationVerifier struct{}

func (observationVerifier) Verify(context.Context, string) (auth.Principal, error) {
	return auth.NewInternal("wallet-service")
}

type observationQueue struct{}

func (observationQueue) Inspect(context.Context, string) (observability.QueueDepth, error) {
	return observability.QueueDepth{}, nil
}
func TestObservedHTTPKeepsAuthenticationAndBoundsRouteLabels(t *testing.T) {
	registry := observability.NewRegistry()
	h := observability.NewHealth(config.TelemetryConfig{Interval: time.Second, Queues: map[string]string{"input": "queue"}}, func(context.Context) (map[string]float64, error) { return nil, nil }, observationQueue{}, registry)
	h.Check(context.Background())
	a := &Authentication{verifier: observationVerifier{}}
	original := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Correlation-ID", "safe-correlation")
		w.WriteHeader(422)
		_, _ = w.Write([]byte("original response"))
	})
	server := ObserveServer(&http.Server{Handler: original}, a, h, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	anonymous := httptest.NewRecorder()
	server.Handler.ServeHTTP(anonymous, httptest.NewRequest("GET", "/metrics", nil))
	if anonymous.Code != 401 {
		t.Fatal("metrics became public on business listener")
	}
	ready := httptest.NewRecorder()
	server.Handler.ServeHTTP(ready, httptest.NewRequest("GET", "/health/ready", nil))
	if ready.Code != 200 {
		t.Fatal("public readiness failed")
	}
	result := httptest.NewRecorder()
	server.Handler.ServeHTTP(result, httptest.NewRequest("GET", "/wallets/private-wallet-id", nil))
	if result.Code != 422 || result.Body.String() != "original response" {
		t.Fatal("observability changed business response")
	}
	metrics := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/metrics", nil)
	request.Header.Set("Authorization", "Bearer token")
	server.Handler.ServeHTTP(metrics, request)
	if metrics.Code != 200 || strings.Contains(metrics.Body.String(), "private-wallet-id") {
		t.Fatal("metrics leaked a high-cardinality identifier")
	}
	if !strings.Contains(metrics.Body.String(), `route="wallet"`) {
		t.Fatal("missing bounded request metric")
	}
}
