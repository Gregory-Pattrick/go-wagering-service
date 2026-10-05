package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

type Database struct {
	pool *pgxpool.Pool
}

func NewDatabase(
	lifecycle fx.Lifecycle,
	cfg config.DatabaseConfig,
	logger *slog.Logger,
) (*Database, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errors.New("DATABASE_URL contains invalid connection settings")
	}

	poolConfig.MaxConns = 10
	poolConfig.MinConns = 0
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "go-wagering-service"
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"

	database := &Database{}

	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			startCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			pool, err := pgxpool.NewWithConfig(startCtx, poolConfig)
			if err != nil {
				return errors.New("initialize PostgreSQL pool")
			}

			if err := pool.Ping(startCtx); err != nil {
				pool.Close()
				return fmt.Errorf("connect to PostgreSQL: %w", err)
			}

			database.pool = pool
			logger.InfoContext(ctx, "PostgreSQL connection established")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if database.pool != nil {
				database.pool.Close()
			}

			logger.InfoContext(ctx, "PostgreSQL pool closed")
			return nil
		},
	})

	return database, nil
}

func (d *Database) Pool() (*pgxpool.Pool, error) {
	if d.pool == nil {
		return nil, errors.New("PostgreSQL pool is not initialized")
	}

	return d.pool, nil
}

func (d *Database) Ping(ctx context.Context) error {
	pool, err := d.Pool()
	if err != nil {
		return err
	}

	return pool.Ping(ctx)
}
