//go:build faultinjection

// Test-only composition: the production worker and consumer algorithms are reused.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/finance"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/sqs"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/testsupport/crashprobe"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	probe, err := crashprobe.New(os.Getenv("PROBE_DIRECTORY"), os.Getenv("PROBE_INSTANCE"), os.Getenv("PROBE_POINT"))
	if err != nil {
		logger.Error("invalid crash probe configuration")
		os.Exit(1)
	}
	options := []fx.Option{
		fx.Supply(logger, probe), fx.Provide(config.LoadDatabase, postgres.NewDatabase),
		fx.WithLogger(func() fxevent.Logger { return &fxevent.SlogLogger{Logger: logger} }),
		fx.StartTimeout(15 * time.Second), fx.StopTimeout(15 * time.Second),
	}
	switch os.Getenv("PROBE_ROLE") {
	case "publisher":
		options = append(options, fx.Provide(config.LoadWorkers, config.LoadPublisher, sqs.NewPublisher), fx.Invoke(publisher))
	case "consumer":
		options = append(options, fx.Provide(config.LoadConsumer, sqs.NewInputQueue), fx.Invoke(consumer))
	default:
		fmt.Fprintln(os.Stderr, "PROBE_ROLE must be publisher or consumer")
		os.Exit(1)
	}
	fx.New(options...).Run()
}
func publisher(lifecycle fx.Lifecycle, db *postgres.Database, sender *sqs.Publisher, cfg config.WorkersConfig, logger *slog.Logger, probe *crashprobe.Probe) {
	source := func() (workers.Backend, error) {
		pool, err := db.Pool()
		if err != nil {
			return nil, err
		}
		return crashprobe.Backend{Backend: finance.New(pool), Probe: probe}, nil
	}
	runner := workers.New(source, crashprobe.Sender{Next: sender, Probe: probe}, cfg, logger)
	workers.Register(lifecycle, runner)
}
func consumer(lifecycle fx.Lifecycle, db *postgres.Database, queue *sqs.InputQueue, cfg config.ConsumerConfig, logger *slog.Logger, probe *crashprobe.Probe) {
	lifecycle.Append(fx.Hook{OnStart: func(context.Context) error { _, err := db.Pool(); return err }})
	handler := sqs.NewRequestHandler(func() (financial.InboxBackend, error) {
		pool, err := db.Pool()
		if err != nil {
			return nil, err
		}
		return finance.New(pool), nil
	}, cfg.Providers)
	workers.RegisterConsumer(lifecycle, workers.NewConsumer(crashprobe.Queue{Queue: queue, Probe: probe}, handler, logger))
}
