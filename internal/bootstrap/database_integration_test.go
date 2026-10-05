//go:build integration

package bootstrap

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"

	"go.uber.org/fx"
)

func TestPostgresLifecycleIntegration(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Fatal("DATABASE_URL is required for integration tests")
	}

	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("LOG_LEVEL", "INFO")

	var database *postgres.Database

	app := New(
		fx.Decorate(func(cfg config.Config) config.Config {
			cfg.HTTPAddress = "127.0.0.1:0"
			return cfg
		}),
		fx.Populate(&database),
	)

	if err := app.Err(); err != nil {
		t.Fatalf("compose application: %v", err)
	}

	t.Cleanup(func() {
		stopApplication(t, app)
	})

	startCtx, cancelStart := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStart()

	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start application with PostgreSQL: %v", err)
	}

	pool, err := database.Pool()
	if err != nil {
		t.Fatalf("get PostgreSQL pool: %v", err)
	}

	queryCtx, cancelQuery := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelQuery()

	var username string
	var superuser bool

	err = pool.QueryRow(queryCtx, `
		SELECT current_user, rolsuper
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&username, &superuser)
	if err != nil {
		t.Fatalf("query PostgreSQL identity: %v", err)
	}

	if username != "wagering_app" || superuser {
		t.Fatalf("unexpected database identity: user=%s superuser=%t", username, superuser)
	}

	stopApplication(t, app)

	checkCtx, cancelCheck := context.WithTimeout(context.Background(), time.Second)
	defer cancelCheck()

	if err := database.Ping(checkCtx); err == nil {
		t.Fatal("PostgreSQL pool remained usable after shutdown")
	}
}
