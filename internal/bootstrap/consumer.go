package bootstrap

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/finance"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/sqs"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"log/slog"
	"time"
)

func NewConsumer(options ...fx.Option) *fx.App {
	base := []fx.Option{
		fx.Module("configuration", fx.Provide(config.Load, config.LoadDatabase, config.LoadConsumer)),
		fx.Module("logging", fx.Provide(newLogger)),
		fx.Module("database", fx.Provide(postgres.NewDatabase)),
		fx.Module("SQS", fx.Provide(sqs.NewInputQueue)),
		fx.Module("consumer", fx.Provide(newRequestConsumer), fx.Invoke(workers.RegisterConsumer)),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: logger} }),
		fx.StartTimeout(15 * time.Second), fx.StopTimeout(15 * time.Second),
	}
	return fx.New(append(base, options...)...)
}
func newRequestConsumer(lifecycle fx.Lifecycle, database *postgres.Database, queue *sqs.InputQueue, c config.ConsumerConfig, logger *slog.Logger) *workers.Consumer {
	// Constructor registration ensures the database starts first and stops last.
	lifecycle.Append(fx.Hook{OnStart: func(context.Context) error { _, err := database.Pool(); return err }})
	handler := sqs.NewRequestHandler(func() (financial.InboxBackend, error) {
		pool, err := database.Pool()
		if err != nil {
			return nil, err
		}
		return finance.New(pool), nil
	}, c.Providers, logger)
	return workers.NewConsumer(queue, handler, logger)
}
