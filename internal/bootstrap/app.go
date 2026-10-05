package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	httpapi "github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/http"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/oidcauth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func New(options ...fx.Option) *fx.App {
	base := []fx.Option{
		fx.Module(
			"config",
			fx.Provide(config.Load, config.LoadDatabase, config.LoadAuth),
		),
		fx.Module(
			"observability",
			fx.Provide(newLogger),
		),
		fx.Module(
			"postgres",
			fx.Provide(postgres.NewDatabase),
			fx.Invoke(func(*postgres.Database) {}),
		),
		fx.Module(
			"authentication",
			fx.Provide(oidcauth.NewVerifier, httpapi.NewAuthentication),
			fx.Invoke(func(*oidcauth.Verifier) {}),
		),
		fx.Module(
			"application",
			fx.Invoke(registerLifecycle),
		),
		fx.Module(
			"http",
			fx.Provide(httpapi.NewRouter, httpapi.NewServer),
			fx.Invoke(func(*http.Server) {}),
		),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
		fx.StartTimeout(15 * time.Second),
		fx.StopTimeout(15 * time.Second),
	}

	return fx.New(append(base, options...)...)
}

func newLogger(cfg config.Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
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
