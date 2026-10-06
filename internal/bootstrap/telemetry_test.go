package bootstrap

import (
	"context"
	"go.uber.org/fx"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/oidcauth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
)

type telemetryTestQueue struct{}

func (telemetryTestQueue) Inspect(context.Context, string) (observability.QueueDepth, error) {
	return observability.QueueDepth{}, nil
}
func TestObservedCompositionStartsAndReleasesBothListeners(t *testing.T) {
	t.Setenv("OBSERVABILITY_ENABLED", "true")
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("LOG_LEVEL", "ERROR")
	var business *http.Server
	var operations *observability.Server
	var health *observability.Health
	app := New(ObserveAPI(),
		fx.Replace(&postgres.Database{}, &oidcauth.Verifier{}),
		fx.Replace(config.TelemetryConfig{Address: "127.0.0.1:0", Interval: time.Second, Queues: map[string]string{"input": "queue"}}),
		fx.Replace(fx.Annotate(telemetryTestQueue{}, fx.As(new(observability.QueueProbe)))),
		fx.Replace(observability.DatabaseProbe(func(context.Context) (map[string]float64, error) { return map[string]float64{}, nil })),
		fx.Decorate(func(c config.Config) config.Config { c.HTTPAddress = "127.0.0.1:0"; return c }),
		fx.Populate(&business, &operations, &health),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()
		_ = app.Stop(cleanup)
	}()
	health.Check(ctx)
	client := &http.Client{Timeout: time.Second}
	for _, address := range []string{business.Addr, operations.HTTP.Addr} {
		response, err := client.Get("http://" + address + "/health/ready")
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("readiness=%d", response.StatusCode)
		}
	}
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{business.Addr, operations.HTTP.Addr} {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			conn.Close()
			t.Fatal("listener still open")
		}
	}
}
