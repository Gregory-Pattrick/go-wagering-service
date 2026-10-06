//go:build financialintegration && tracingintegration

package finance

import (
	"context"
	"errors"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/observability/tracing"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestOutboxTraceMetadataSharesFinancialCommit(t *testing.T) {
	migrationURL, appURL := os.Getenv("FINANCE_TEST_MIGRATION_URL"), os.Getenv("FINANCE_TEST_DATABASE_URL")
	for _, raw := range []string{migrationURL, appURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() != "finance-postgres" || u.Path != "/wagering" {
			t.Fatal("refusing reset outside finance-postgres/wagering")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if e := migrations.Run(ctx, migrationURL, 0); e != nil {
		t.Fatal(e)
	}
	if e := migrations.Run(ctx, migrationURL, migrations.Latest()); e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.New(ctx, appURL)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	store := New(pool)
	parent := "00-11111111111111111111111111111111-2222222222222222-01"
	traced := tracing.Extract(ctx, map[string]string{"traceparent": parent})
	opening := id(t)
	if e = store.Within(traced, func(u *Unit) error { return u.OpenWallet(traced, newWallet(t, 10000), opening, id(t), meta(t)) }); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM wagering.outbox_trace_context t JOIN wagering.outbox o USING(event_id) WHERE o.transaction_id=$1 AND t.traceparent=$2 AND NOT (o.payload ? 'traceparent')`, opening, parent).Scan(&count); e != nil || count != 2 {
		t.Fatalf("committed trace metadata count=%d error=%v", count, e)
	}
	rolledBack := id(t)
	injected := errors.New("rollback fixture")
	e = store.Within(traced, func(u *Unit) error {
		if e := u.OpenWallet(traced, newWallet(t, 10000), rolledBack, id(t), meta(t)); e != nil {
			return e
		}
		return injected
	})
	if !errors.Is(e, injected) {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM wagering.outbox_trace_context").Scan(&count); e != nil || count != 2 {
		t.Fatalf("trace metadata escaped rollback: %d %v", count, e)
	}
	if e = store.Within(ctx, func(u *Unit) error { return u.OpenWallet(ctx, newWallet(t, 10000), id(t), id(t), meta(t)) }); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM wagering.outbox_trace_context").Scan(&count); e != nil || count != 2 {
		t.Fatal("disabled tracing persisted metadata", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE wagering.outbox_trace_context SET tracestate='changed'"); e == nil {
		t.Fatal("trace origin can be overwritten")
	}
}
