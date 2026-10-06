//go:build financialintegration && workerintegration

package finance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	sqsadapter "github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/sqs"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/config"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/workers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkerIntegration(t *testing.T) {
	migrationURL, appURL := os.Getenv("FINANCE_TEST_MIGRATION_URL"), os.Getenv("FINANCE_TEST_DATABASE_URL")
	for _, raw := range []string{migrationURL, appURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() != "finance-postgres" || parsed.Path != "/wagering" {
			t.Fatal("refusing reset outside dedicated finance-postgres/wagering")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	open := func() wallet.Wallet {
		t.Helper()
		w := newWallet(t, 10000)
		if err := store.Within(ctx, func(u *Unit) error { return u.OpenWallet(ctx, w, id(t), id(t), meta(t)) }); err != nil {
			t.Fatal(err)
		}
		return w
	}
	submit := func(tr tx.Transaction) {
		t.Helper()
		if err := store.Within(ctx, func(u *Unit) error { _, err := execute(ctx, u, tr, id(t), meta(t)); return err }); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(tr tx.Transaction) Work {
		t.Helper()
		s, _ := tr.Snapshot()
		exec("UPDATE wagering.transaction_work SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE transaction_id=$1", s.Input.ID)
		w, err := store.ClaimReference(ctx, id(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if w.ID != s.Input.ID {
			t.Fatal("claimed unexpected work")
		}
		return w
	}
	assertState := func(tr tx.Transaction, want tx.Status, code tx.FailureCode) {
		t.Helper()
		s, _ := tr.Snapshot()
		var status, failure string
		if err := pool.QueryRow(ctx, "SELECT status,failure_code FROM wagering.wager_transactions WHERE id=$1", s.Input.ID).Scan(&status, &failure); err != nil {
			t.Fatal(err)
		}
		if status != string(want) || failure != string(code) {
			t.Fatalf("status=%s failure=%s", status, failure)
		}
	}

	t.Run("waiting retries do not duplicate events and later reference resumes once", func(t *testing.T) {
		w := open()
		original := operation(t, w, tx.Bet, 8000, "")
		os, _ := original.Snapshot()
		refund := operation(t, w, tx.Refund, 8000, os.Input.ExternalTransactionID)
		rs, _ := refund.Snapshot()
		submit(refund)
		first := claim(refund)
		status, err := store.ResumeReference(ctx, first, time.Second, 15*time.Minute)
		if err != nil || status != "PENDING_REFERENCE" {
			t.Fatalf("%s %v", status, err)
		}
		if count("SELECT count(*) FROM wagering.outbox WHERE transaction_id=$1", rs.Input.ID) != 1 {
			t.Fatal("duplicate pending event")
		}
		if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE transaction_id=$1", rs.Input.ID) != 0 {
			t.Fatal("pending reference changed balance")
		}
		submit(original)
		second := claim(refund)
		// A new connection pool represents another process taking durable work.
		restarted, err := pgxpool.New(ctx, appURL)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		if status, err = New(restarted).ResumeReference(ctx, second, time.Second, 15*time.Minute); err != nil || status != "PROCESSED" {
			t.Fatalf("%s %v", status, err)
		}
		if _, err = store.ResumeReference(ctx, second, time.Second, 15*time.Minute); !errors.Is(err, delivery.ErrLeaseLost) {
			t.Fatal("old claim was reusable", err)
		}
		assertState(refund, tx.Processed, "")
		if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE transaction_id=$1", rs.Input.ID) != 1 || count("SELECT count(*) FROM wagering.outbox WHERE transaction_id=$1", rs.Input.ID) != 3 {
			t.Fatal("incorrect final ledger/events")
		}
		got, err := store.Wallet(ctx, rs.Input.WalletID)
		if err != nil {
			t.Fatal(err)
		}
		gs, _ := got.Snapshot()
		balance, _ := gs.Balance.MinorUnits()
		if balance != 10000 || gs.Version != 3 {
			t.Fatal("incorrect resumed balance")
		}
	})
	t.Run("expiry and unsuccessful reference reject without movement", func(t *testing.T) {
		for _, mode := range []string{"absent", "pending", "rejected", "mismatch"} {
			t.Run(mode, func(t *testing.T) {
				w := open()
				original := operation(t, w, tx.Bet, 20000, "")
				os, _ := original.Snapshot()
				refund := operation(t, w, tx.Refund, 20000, os.Input.ExternalTransactionID)
				rs, _ := refund.Snapshot()
				submit(refund)
				ttl := 15 * time.Minute
				want := tx.ReferenceNotProcessed
				switch mode {
				case "absent":
					ttl = time.Nanosecond
					want = tx.ReferenceNotFound
				case "rejected":
					submit(original)
				case "mismatch":
					altered := os.Input
					altered.RoundID = "other-round"
					other, err := tx.NewExternal(altered, os.CreatedAt)
					if err != nil {
						t.Fatal(err)
					}
					submit(other)
					want = tx.ReferenceMismatch
				case "pending":
					// A WIN waiting for a missing BET remains durable itself. Use an outer
					// ROLLBACK so the reference kind is eligible before checking its status.
					nested := operation(t, w, tx.Win, 20000, id(t))
					ns, _ := nested.Snapshot()
					submit(nested)
					// Keep nested work out of the explicit outer claim, even on slow CI.
					exec("UPDATE wagering.transaction_work SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE transaction_id=$1", ns.Input.ID)
					// Remove the first absent refund by expiring it, then create the rollback.
					if _, err := store.ResumeReference(ctx, claim(refund), time.Second, time.Nanosecond); err != nil {
						t.Fatal(err)
					}
					refund = operation(t, w, tx.Rollback, 20000, ns.Input.ExternalTransactionID)
					rs, _ = refund.Snapshot()
					submit(refund)
					// Prevent the nested work from competing with the explicit outer claim.
					exec("UPDATE wagering.transaction_work SET next_attempt_at=clock_timestamp()+interval '1 hour' WHERE transaction_id=$1", ns.Input.ID)
					first := claim(refund)
					status, err := store.ResumeReference(ctx, first, time.Second, ttl)
					if err != nil || status != "PENDING_REFERENCE" {
						t.Fatalf("pending original: %s %v", status, err)
					}
					ttl = time.Nanosecond
					want = tx.ReferenceNotFound
					defer func() {
						if _, err := store.ResumeReference(ctx, claim(nested), time.Second, time.Nanosecond); err != nil {
							t.Error(err)
						}
					}()
				}
				if _, err := store.ResumeReference(ctx, claim(refund), time.Second, ttl); err != nil {
					t.Fatal(err)
				}
				assertState(refund, tx.Rejected, want)
				if count("SELECT count(*) FROM wagering.transaction_work WHERE transaction_id=$1", rs.Input.ID) != 0 || count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE transaction_id=$1", rs.Input.ID) != 0 {
					t.Fatal("rejected reference retained work or ledger")
				}
			})
		}
	})
	t.Run("concurrent claims and expired owner fencing", func(t *testing.T) {
		w := open()
		pending := operation(t, w, tx.Refund, 100, id(t))
		ps, _ := pending.Snapshot()
		submit(pending)
		exec("UPDATE wagering.transaction_work SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE transaction_id=$1", ps.Input.ID)
		start := make(chan struct{})
		results := make(chan Work, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		owners := []string{id(t), id(t)}
		for _, owner := range owners {
			wg.Add(1)
			go func(owner string) {
				defer wg.Done()
				<-start
				w, err := store.ClaimReference(ctx, owner, 30*time.Second)
				results <- w
				errs <- err
			}(owner)
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		wins, empty := 0, 0
		for err := range errs {
			if err == nil {
				wins++
			} else if errors.Is(err, pgx.ErrNoRows) {
				empty++
			} else {
				t.Fatal(err)
			}
		}
		if wins != 1 || empty != 1 {
			t.Fatal("claim not exclusive")
		}
		var old Work
		for result := range results {
			if result.ID != "" {
				old = result
			}
		}
		exec("UPDATE wagering.transaction_work SET lease_until=clock_timestamp()-interval '1 second' WHERE transaction_id=$1", old.ID)
		replacement := claim(pending)
		if _, err := store.ResumeReference(ctx, old, time.Second, 15*time.Minute); !errors.Is(err, delivery.ErrLeaseLost) {
			t.Fatal("stale owner modified work", err)
		}
		if _, err := store.ResumeReference(ctx, replacement, time.Second, time.Nanosecond); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("outbox claim recovery preserves snapshot and fences old publisher", func(t *testing.T) {
		// Acknowledge previous fixtures so this case controls its candidate set.
		exec("UPDATE wagering.outbox SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE published_at IS NULL")
		w := open()
		ws, _ := w.Snapshot()
		first, err := store.ClaimEvent(ctx, id(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if first.AggregateID != ws.ID {
			t.Fatal("unexpected aggregate")
		}
		if _, err := store.ClaimEvent(ctx, id(t), 30*time.Second); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("leased head must block later events for this wallet", err)
		}
		exec("UPDATE wagering.outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE event_id=$1", first.ID)
		second, err := store.ClaimEvent(ctx, id(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID != second.ID || !bytes.Equal(first.Payload, second.Payload) || second.Attempts != 2 {
			t.Fatal("recovery changed snapshot or identity")
		}
		if err := store.ConfirmEvent(ctx, first); !errors.Is(err, delivery.ErrLeaseLost) {
			t.Fatal("stale publisher acknowledged", err)
		}
		if err := store.ConfirmEvent(ctx, second); err != nil {
			t.Fatal(err)
		}
		next, err := store.ClaimEvent(ctx, id(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RetryEvent(ctx, next, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimEvent(ctx, id(t), 30*time.Second); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("backoff was ignored", err)
		}
	})
	t.Run("two publishers claim independent wallets without duplicate ownership", func(t *testing.T) {
		exec("UPDATE wagering.outbox SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE published_at IS NULL")
		_ = open()
		_ = open()
		start := make(chan struct{})
		results := make(chan delivery.Event, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		owners := []string{id(t), id(t)}
		for _, owner := range owners {
			wg.Add(1)
			go func(owner string) {
				defer wg.Done()
				<-start
				e, err := store.ClaimEvent(ctx, owner, 30*time.Second)
				results <- e
				errs <- err
			}(owner)
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		seen := map[string]bool{}
		aggregates := map[string]bool{}
		for e := range results {
			if seen[e.ID] || aggregates[e.AggregateID] {
				t.Fatal("duplicate ownership or blocked independent wallet")
			}
			seen[e.ID] = true
			aggregates[e.AggregateID] = true
			if err := store.ConfirmEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("real SQS publish and recovery after send before database confirmation", func(t *testing.T) {
		exec("UPDATE wagering.outbox SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE published_at IS NULL")
		_ = open()
		cfg, err := config.LoadPublisher()
		if err != nil {
			t.Fatal(err)
		}
		sender, err := sqsadapter.NewPublisher(cfg)
		if err != nil {
			t.Fatal(err)
		}
		first, err := store.ClaimEvent(ctx, id(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		// Intentionally omit ConfirmEvent after a real accepted SQS send.
		if err := sender.Send(ctx, first); err != nil {
			t.Fatal(err)
		}
		exec("UPDATE wagering.outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE event_id=$1", first.ID)
		second, err := store.ClaimEvent(ctx, id(t), 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if first.ID != second.ID || !bytes.Equal(first.Payload, second.Payload) {
			t.Fatal("send/ack recovery changed identity")
		}
		if err := sender.Send(ctx, second); err != nil {
			t.Fatal(err)
		}
		if err := store.ConfirmEvent(ctx, second); err != nil {
			t.Fatal(err)
		}
		if count("SELECT count(*) FROM wagering.outbox WHERE event_id=$1 AND published_at IS NOT NULL AND attempts=2", first.ID) != 1 {
			t.Fatal("recovered publication not recorded")
		}
		runner := workers.New(func() (workers.Backend, error) { return store, nil }, sender, config.WorkersConfig{Lease: 30 * time.Second, RetryBase: time.Second, RetryCap: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if found, err := runner.PublishOne(ctx); err != nil || !found {
			t.Fatalf("publish remaining event: %v %v", found, err)
		}
		if count("SELECT count(*) FROM wagering.outbox WHERE published_at IS NULL") != 0 {
			t.Fatal("outbox did not drain")
		}
	})
}
