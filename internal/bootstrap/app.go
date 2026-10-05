package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func New() *fx.App {
	return fx.New(
		fx.Module(
			"config",
			fx.Provide(config.Load),
		),
		fx.Module(
			"observability",
			fx.Provide(newLogger),
		),
		fx.Module(
			"application",
			fx.Invoke(registerLifecycle),
		),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
		fx.StartTimeout(15*time.Second),
		fx.StopTimeout(15*time.Second),
	)
}

func newLogger(cfg config.Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})

	return slog.New(handler).With("service", "go-wagering-service")
}

func registerLifecycle(
	lifecycle fx.Lifecycle,
	logger *slog.Logger,
	cfg config.Config,
) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.InfoContext(
				ctx,
				"application starting",
				"httpAddress",
				cfg.HTTPAddress,
			)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.InfoContext(ctx, "application stopping")
			return nil
		},
	})
}
