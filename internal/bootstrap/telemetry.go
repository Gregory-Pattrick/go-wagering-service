package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	httpapi "github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/http"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/finance"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/sqs"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"
)

func telemetryCommon(api bool) fx.Option {
	return fx.Options(
		fx.Provide(config.LoadTelemetry, observability.NewRegistry, sqs.NewMonitor),
		fx.Provide(func(db *postgres.Database) observability.DatabaseProbe {
			return func(ctx context.Context) (map[string]float64, error) {
				if !api {
					return nil, db.Ping(ctx)
				}
				pool, err := db.Pool()
				if err != nil {
					return nil, err
				}
				return finance.New(pool).Telemetry(ctx)
			}
		}),
		fx.Provide(func(c config.TelemetryConfig, db observability.DatabaseProbe, q observability.QueueProbe, r *observability.Registry) *observability.Health {
			c.CollectDatabaseStats = api
			return observability.NewHealth(c, db, q, r)
		}),
		fx.Invoke(observability.RegisterHealth),
		fx.Provide(observability.NewServer), fx.Invoke(func(*observability.Server) {}),
	)
}
func observeAPI() fx.Option {
	return fx.Options(telemetryCommon(true),
		fx.Decorate(func(s *financial.Service, r *observability.Registry) *financial.Service {
			return financial.ObserveService(s, r)
		}),
		fx.Decorate(func(s *http.Server, a *httpapi.Authentication, h *observability.Health, r *observability.Registry, l *slog.Logger) *http.Server {
			return httpapi.ObserveServer(s, a, h, r, l)
		}),
		fx.Invoke(func(s *financial.Service, r *observability.Registry) {
			r.Prom.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "wagering_reconciliation_mismatches_total", Help: "Reconciliation discrepancies observed by this API process."}, func() float64 { return float64(s.ReconciliationMismatches()) }))
		}),
	)
}
func observeWorkers() fx.Option {
	return fx.Options(telemetryCommon(false), fx.Decorate(workers.ObserveRunner))
}
func observeConsumer() fx.Option {
	return fx.Options(telemetryCommon(false), fx.Decorate(workers.ObserveConsumer))
}

// The dedicated Compose override enables telemetry. Earlier development commands
// remain usable without requiring monitor credentials or extra listeners.
func optionalTelemetry(build func() fx.Option) fx.Option {
	switch os.Getenv("OBSERVABILITY_ENABLED") {
	case "", "false":
		return fx.Options()
	case "true":
		return build()
	default:
		return fx.Error(fmt.Errorf("OBSERVABILITY_ENABLED must be true or false"))
	}
}
func ObserveAPI() fx.Option      { return optionalTelemetry(observeAPI) }
func ObserveWorkers() fx.Option  { return optionalTelemetry(observeWorkers) }
func ObserveConsumer() fx.Option { return optionalTelemetry(observeConsumer) }
