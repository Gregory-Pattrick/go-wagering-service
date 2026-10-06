package bootstrap

import (
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/finance"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/sqs"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"log/slog"
	"time"
)

func NewWorkers(options ...fx.Option) *fx.App {
	base := []fx.Option{
		fx.Module("configuration", fx.Provide(config.Load, config.LoadDatabase, config.LoadWorkers, config.LoadPublisher)),
		fx.Module("logging", fx.Provide(newLogger)),
		fx.Module("database", fx.Provide(postgres.NewDatabase)),
		fx.Module("publisher", fx.Provide(sqs.NewPublisher)),
		fx.Module("workers", fx.Provide(newWorkers), fx.Invoke(workers.Register)),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: logger} }),
		fx.StartTimeout(15 * time.Second), fx.StopTimeout(15 * time.Second),
	}
	return fx.New(append(base, options...)...)
}
func newWorkers(database *postgres.Database, publisher *sqs.Publisher, c config.WorkersConfig, logger *slog.Logger) *workers.Runner {
	return workers.New(func() (workers.Backend, error) {
		pool, err := database.Pool()
		if err != nil {
			return nil, err
		}
		return finance.New(pool), nil
	}, publisher, c, logger)
}
