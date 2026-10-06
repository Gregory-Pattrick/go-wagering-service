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
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"
)

func enabled(name string) (bool, error) {
	switch os.Getenv(name) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}
func instrumentation(role string) fx.Option {
	metrics, err := enabled("OBSERVABILITY_ENABLED")
	if err != nil {
		return fx.Error(err)
	}
	traces, err := enabled("TRACING_ENABLED")
	if err != nil {
		return fx.Error(err)
	}
	if !metrics && !traces {
		return fx.Options()
	}
	options := []fx.Option{}
	if metrics {
		options = append(options, telemetryCommon(role == "api"))
	}
	if traces {
		options = append(options,
			fx.Decorate(func(l *slog.Logger) *slog.Logger { return slog.New(tracing.LogHandler{Handler: l.Handler()}) }),
			fx.Provide(config.LoadTracing, func(lc fx.Lifecycle, c config.TracingConfig, l *slog.Logger) (*tracing.Runtime, error) {
				return tracing.New(lc, c, l, "wagering-"+role)
			}),
			fx.Provide(func(db *postgres.Database) tracing.Reader { return traceReader{db} }),
		)
	}
	// One decorator per type composes both features, avoiding duplicate Fx decorators.
	switch role {
	case "api":
		options = append(options, fx.Decorate(instrumentService), fx.Decorate(instrumentHTTP))
		if metrics {
			options = append(options, fx.Invoke(func(s *financial.Service, r *observability.Registry) {
				r.Prom.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "wagering_reconciliation_mismatches_total", Help: "Reconciliation discrepancies observed by this API process."}, func() float64 { return float64(s.ReconciliationMismatches()) }))
			}))
		}
	case "workers":
		options = append(options, fx.Decorate(instrumentWorkers))
	case "consumer":
		options = append(options, fx.Decorate(instrumentConsumer))
	}
	return fx.Options(options...)
}
func InstrumentAPI() fx.Option      { return instrumentation("api") }
func InstrumentWorkers() fx.Option  { return instrumentation("workers") }
func InstrumentConsumer() fx.Option { return instrumentation("consumer") }

type serviceInstrumentation struct {
	fx.In
	Service  *financial.Service
	Registry *observability.Registry `optional:"true"`
	Tracing  *tracing.Runtime        `optional:"true"`
}

func instrumentService(p serviceInstrumentation) *financial.Service {
	s := p.Service
	if p.Registry != nil {
		s = financial.ObserveService(s, p.Registry)
	}
	if p.Tracing != nil {
		s = financial.TraceService(s, p.Tracing)
	}
	return s
}

type httpInstrumentation struct {
	fx.In
	Server         *http.Server
	Authentication *httpapi.Authentication
	Logger         *slog.Logger
	Registry       *observability.Registry `optional:"true"`
	Health         *observability.Health   `optional:"true"`
	Tracing        *tracing.Runtime        `optional:"true"`
}

func instrumentHTTP(p httpInstrumentation) *http.Server {
	s := p.Server
	if p.Registry != nil {
		s = httpapi.ObserveServer(s, p.Authentication, p.Health, p.Registry, p.Logger)
	}
	if p.Tracing != nil {
		s = httpapi.TraceServer(s, p.Tracing)
	}
	return s
}

type workerInstrumentation struct {
	fx.In
	Runner   *workers.Runner
	Registry *observability.Registry `optional:"true"`
	Tracing  *tracing.Runtime        `optional:"true"`
	Reader   tracing.Reader          `optional:"true"`
}

func instrumentWorkers(p workerInstrumentation) *workers.Runner {
	r := p.Runner
	if p.Registry != nil {
		r = workers.ObserveRunner(r, p.Registry)
	}
	if p.Tracing != nil {
		r = workers.TraceRunner(r, p.Tracing, p.Reader)
	}
	return r
}

type consumerInstrumentation struct {
	fx.In
	Consumer *workers.Consumer
	Registry *observability.Registry `optional:"true"`
	Tracing  *tracing.Runtime        `optional:"true"`
}

func instrumentConsumer(p consumerInstrumentation) *workers.Consumer {
	c := p.Consumer
	if p.Registry != nil {
		c = workers.ObserveConsumer(c, p.Registry)
	}
	if p.Tracing != nil {
		c = workers.TraceConsumer(c, p.Tracing)
	}
	return c
}

type traceReader struct{ db *postgres.Database }

func (r traceReader) EventCarrier(ctx context.Context, id string) (map[string]string, error) {
	pool, e := r.db.Pool()
	if e != nil {
		return nil, e
	}
	return finance.New(pool).EventCarrier(ctx, id)
}
func (r traceReader) ReferenceCarrier(ctx context.Context, id string) (map[string]string, error) {
	pool, e := r.db.Pool()
	if e != nil {
		return nil, e
	}
	return finance.New(pool).ReferenceCarrier(ctx, id)
}
