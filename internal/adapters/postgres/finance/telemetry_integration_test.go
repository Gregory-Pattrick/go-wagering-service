//go:build financialintegration && telemetryintegration

package finance

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestTelemetryIntegration(t *testing.T) {
	migrationURL, appURL := os.Getenv("FINANCE_TEST_MIGRATION_URL"), os.Getenv("FINANCE_TEST_DATABASE_URL")
	for _, raw := range []string{migrationURL, appURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != "finance-postgres" || u.Path != "/wagering" {
			t.Fatal("refusing reset outside finance-postgres/wagering")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := migrations.Run(ctx, migrationURL, 0); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(ctx, migrationURL, migrations.Latest()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := New(pool)
	w := newWallet(t, 10000)
	if err := store.Within(ctx, func(u *Unit) error { return u.OpenWallet(ctx, w, id(t), id(t), meta(t)) }); err != nil {
		t.Fatal(err)
	}
	pending := operation(t, w, tx.Refund, 100, id(t))
	if err := store.Within(ctx, func(u *Unit) error { _, err := execute(ctx, u, pending, id(t), meta(t)); return err }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		snapshot, err := store.Telemetry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]float64{"transactions_processed": 1, "transactions_pending_reference": 1, "outbox_pending": 3, "references_pending": 1, "inbox_completed": 0} {
			if snapshot[name] != want {
				t.Fatalf("%s=%v want=%v", name, snapshot[name], want)
			}
		}
	}
	before, _ := w.Snapshot()
	after, err := store.Wallet(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := after.Snapshot()
	n, _ := state.Balance.MinorUnits()
	if n != 10000 || state.Version != 1 {
		t.Fatal("telemetry changed financial state")
	}
}
