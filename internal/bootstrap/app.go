package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func New() *fx.App {
	return fx.New(
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

func newLogger() *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	return slog.New(handler).With("service", "go-wagering-service")
}

func registerLifecycle(lifecycle fx.Lifecycle, logger *slog.Logger) {
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.InfoContext(ctx, "application starting")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.InfoContext(ctx, "application stopping")
			return nil
		},
	})
}
