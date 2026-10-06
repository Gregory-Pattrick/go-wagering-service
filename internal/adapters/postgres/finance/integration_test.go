//go:build financialintegration

package finance

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func id(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func instant() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
func cash(t *testing.T, n int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(n, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func meta(t *testing.T) events.Metadata {
	return events.Metadata{TransactionEventID: id(t), BalanceEventID: id(t), CorrelationID: "financial-test"}
}
func newWallet(t *testing.T, n int64) wallet.Wallet {
	t.Helper()
	w, err := wallet.New(id(t), id(t), cash(t, n), instant())
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func operation(t *testing.T, w wallet.Wallet, kind tx.Kind, n int64, reference string) tx.Transaction {
	t.Helper()
	s, _ := w.Snapshot()
	tr, err := tx.NewExternal(tx.ExternalInput{ID: id(t), ExternalTransactionID: id(t), ProviderID: "provider-a", IdempotencyKey: id(t), WalletID: s.ID, PlayerID: s.PlayerID, RoundID: "round", GameID: "game", Kind: kind, Money: cash(t, n), ReferenceExternalTransactionID: reference}, instant())
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func execute(ctx context.Context, u *Unit, tr tx.Transaction, entryID string, metadata events.Metadata) (processing.Decision, error) {
	if err := u.InsertTransaction(ctx, tr); err != nil {
		return processing.Decision{}, err
	}
	s, _ := tr.Snapshot()
	w, err := u.LockWallet(ctx, s.Input.WalletID)
	if err != nil {
		return processing.Decision{}, err
	}
	var ref *processing.Reference
	if s.Input.ReferenceExternalTransactionID != "" {
		ref, err = u.Reference(ctx, s.Input.ProviderID, s.Input.ReferenceExternalTransactionID, s.Input.WalletID)
		if err != nil {
			return processing.Decision{}, err
		}
	}
	d, err := processing.Evaluate(processing.Request{Transaction: tr, Wallet: w, Reference: ref, LedgerID: entryID, Now: instant()})
	if err != nil {
		return d, err
	}
	schedule := &Schedule{NextAttemptAt: instant().Add(time.Second), ExpiresAt: instant().Add(15 * time.Minute)}
	return d, u.SaveDecision(ctx, tr, d, metadata, schedule)
}
func TestFinancialIntegration(t *testing.T) {
	migrationURL, appURL := os.Getenv("FINANCE_TEST_MIGRATION_URL"), os.Getenv("FINANCE_TEST_DATABASE_URL")
	for _, raw := range []string{migrationURL, appURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() != "finance-postgres" || parsed.Path != "/wagering" {
			t.Fatal("financial tests require the dedicated finance-postgres/wagering container; refusing reset")
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
	if err := migrations.Run(ctx, migrationURL, migrations.Latest()); err != nil {
		t.Fatal("idempotent migration:", err)
	}
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := New(pool)
	open := func(w wallet.Wallet) {
		t.Helper()
		opening, entry, m := id(t), id(t), meta(t)
		if err := store.Within(ctx, func(u *Unit) error { return u.OpenWallet(ctx, w, opening, entry, m) }); err != nil {
			t.Fatal(err)
		}
	}
	assertWallet := func(w wallet.Wallet, want int64, version int64) {
		t.Helper()
		s, _ := w.Snapshot()
		got, err := store.Wallet(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, _ := got.Snapshot()
		n, _ := snapshot.Balance.MinorUnits()
		if n != want || snapshot.Version != version {
			t.Fatalf("balance=%d version=%d", n, snapshot.Version)
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
	t.Run("opening and balanced journal", func(t *testing.T) {
		w := newWallet(t, 10000)
		open(w)
		s, _ := w.Snapshot()
		assertWallet(w, 10000, 1)
		if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE wallet_id=$1", s.ID) != 1 || count("SELECT count(*) FROM wagering.outbox WHERE aggregate_id=$1", s.ID) != 2 {
			t.Fatal("missing opening records")
		}
		if count(`SELECT count(*) FROM wagering.postings p JOIN wagering.journals j USING(transaction_id) JOIN wagering.wallet_ledger_entries e ON e.id=j.ledger_id WHERE e.wallet_id=$1`, s.ID) != 2 {
			t.Fatal("missing balanced postings")
		}
		zero := newWallet(t, 0)
		open(zero)
		z, _ := zero.Snapshot()
		if count("SELECT count(*) FROM wagering.wager_transactions WHERE wallet_id=$1", z.ID) != 0 {
			t.Fatal("zero opening produced transaction")
		}
		duplicate, err := wallet.New(id(t), s.PlayerID, cash(t, 0), instant())
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Within(ctx, func(u *Unit) error { return u.OpenWallet(ctx, duplicate, "", "", events.Metadata{}) }); err == nil {
			t.Fatal("duplicate player/currency accepted")
		}
	})
	t.Run("rollback after all writes", func(t *testing.T) {
		w := newWallet(t, 1000)
		s, _ := w.Snapshot()
		opening, entry, m := id(t), id(t), meta(t)
		injected := errors.New("injected before commit")
		err := store.Within(ctx, func(u *Unit) error {
			if err := u.OpenWallet(ctx, w, opening, entry, m); err != nil {
				return err
			}
			return injected
		})
		if !errors.Is(err, injected) {
			t.Fatal(err)
		}
		for _, query := range []string{"SELECT count(*) FROM wagering.wallets WHERE id=$1", "SELECT count(*) FROM wagering.wallet_ledger_entries WHERE wallet_id=$1", "SELECT count(*) FROM wagering.outbox WHERE aggregate_id=$1"} {
			if count(query, s.ID) != 0 {
				t.Fatal("partial financial commit")
			}
		}
	})
	t.Run("deferred constraints reject incomplete financial data", func(t *testing.T) {
		w := newWallet(t, 1000)
		open(w)
		s, _ := w.Snapshot()
		if err := store.Within(ctx, func(u *Unit) error {
			_, err := u.tx.Exec(ctx, "UPDATE wagering.wallets SET balance_minor=900,version=2 WHERE id=$1", s.ID)
			return err
		}); err == nil {
			t.Fatal("balance changed without ledger")
		}
		assertWallet(w, 1000, 1)
		for _, mode := range []string{"missing-journal", "missing-posting", "wrong-account", "missing-event"} {
			tr := operation(t, w, tx.Bet, 100, "")
			entryID, m := id(t), meta(t)
			err := store.Within(ctx, func(u *Unit) error {
				if err := u.InsertTransaction(ctx, tr); err != nil {
					return err
				}
				locked, err := u.LockWallet(ctx, s.ID)
				if err != nil {
					return err
				}
				d, err := processing.Evaluate(processing.Request{Transaction: tr, Wallet: locked, LedgerID: entryID, Now: instant()})
				if err != nil {
					return err
				}
				if err = u.SaveWallet(ctx, d.Wallet); err != nil {
					return err
				}
				if err = u.SaveTransaction(ctx, tr, d.Transaction); err != nil {
					return err
				}
				if err = u.AppendEntry(ctx, *d.Entry, 2); err != nil {
					return err
				}
				if mode != "missing-journal" {
					if mode == "missing-event" {
						if err = u.AppendJournal(ctx, *d.Entry, tx.Bet); err != nil {
							return err
						}
					} else {
						e, _ := d.Entry.Snapshot()
						if _, err = u.tx.Exec(ctx, "INSERT INTO wagering.journals VALUES($1,$2,$3)", e.TransactionID, e.ID, e.CreatedAt); err != nil {
							return err
						}
						if _, err = u.tx.Exec(ctx, `INSERT INTO wagering.postings VALUES($1,'DEBIT',$2,100,'BRL')`, e.TransactionID, "wallet:"+s.ID); err != nil {
							return err
						}
						if mode == "wrong-account" {
							if _, err = u.tx.Exec(ctx, `INSERT INTO wagering.postings VALUES($1,'CREDIT','opening:BRL',100,'BRL')`, e.TransactionID); err != nil {
								return err
							}
						}
					}
				}
				if mode != "missing-event" {
					batch, err := events.ForTransition(tr, d.Transaction, d.Entry, m)
					if err != nil {
						return err
					}
					return u.AddEvents(ctx, snapshotID(d.Transaction), batch)
				}
				return nil
			})
			if err == nil {
				t.Fatalf("%s committed", mode)
			}
			assertWallet(w, 1000, 1)
		}
	})
	t.Run("application cannot mutate accounting or event payloads", func(t *testing.T) {
		for _, query := range []string{"UPDATE wagering.wallet_ledger_entries SET amount_minor=amount_minor", "DELETE FROM wagering.wallet_ledger_entries", "TRUNCATE wagering.wallet_ledger_entries", "UPDATE wagering.postings SET amount_minor=amount_minor", "DELETE FROM wagering.journals", "TRUNCATE wagering.postings", "UPDATE wagering.outbox SET payload='{}'::jsonb", "DELETE FROM wagering.outbox"} {
			if _, err := pool.Exec(ctx, query); err == nil {
				t.Fatalf("forbidden SQL accepted: %s", query)
			}
		}
	})
	t.Run("concurrent bets and independent wallets", func(t *testing.T) {
		w := newWallet(t, 10000)
		open(w)
		inputs := []tx.Transaction{operation(t, w, tx.Bet, 8000, ""), operation(t, w, tx.Bet, 8000, "")}
		entries := []string{id(t), id(t)}
		metadata := []events.Metadata{meta(t), meta(t)}
		results := make(chan tx.Status, 2)
		errs := make(chan error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range inputs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				var d processing.Decision
				err := store.Within(ctx, func(u *Unit) error {
					var err error
					d, err = execute(ctx, u, inputs[i], entries[i], metadata[i])
					return err
				})
				errs <- err
				if err == nil {
					s, _ := d.Transaction.Snapshot()
					results <- s.Status
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(errs)
		close(results)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		statuses := map[tx.Status]int{}
		for status := range results {
			statuses[status]++
		}
		if statuses[tx.Processed] != 1 || statuses[tx.Rejected] != 1 {
			t.Fatal(statuses)
		}
		assertWallet(w, 2000, 2)
		s, _ := w.Snapshot()
		if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE wallet_id=$1 AND direction='DEBIT'", s.ID) != 1 {
			t.Fatal("duplicate debit")
		}
		other := newWallet(t, 0)
		open(other)
		hold, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Rollback(ctx)
		if _, err = hold.Exec(ctx, "SELECT id FROM wagering.wallets WHERE id=$1 FOR UPDATE", s.ID); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		tr := operation(t, other, tx.Win, 100, "")
		entry, m := id(t), meta(t)
		if err = store.Within(bounded, func(u *Unit) error { _, err := execute(bounded, u, tr, entry, m); return err }); err != nil {
			t.Fatal("unrelated wallet blocked:", err)
		}
	})
	t.Run("identity conflicts preserve original result", func(t *testing.T) {
		w := newWallet(t, 10000)
		open(w)
		bet := operation(t, w, tx.Bet, 8000, "")
		entry, m := id(t), meta(t)
		if err := store.Within(ctx, func(u *Unit) error { _, err := execute(ctx, u, bet, entry, m); return err }); err != nil {
			t.Fatal(err)
		}
		win := operation(t, w, tx.Win, 3000, "")
		entry, m = id(t), meta(t)
		if err := store.Within(ctx, func(u *Unit) error { _, err := execute(ctx, u, win, entry, m); return err }); err != nil {
			t.Fatal(err)
		}
		b, _ := bet.Snapshot()
		if err := store.Within(ctx, func(u *Unit) error {
			if err := u.InsertTransaction(ctx, bet); !errors.Is(err, ErrConflict) {
				return fmt.Errorf("expected duplicate conflict: %v", err)
			}
			original, err := u.ByKey(ctx, b.Input.ProviderID, b.Input.IdempotencyKey)
			if err != nil {
				return err
			}
			s, _ := original.Snapshot()
			n, _ := s.Result.Balance.MinorUnits()
			if n != 2000 {
				return fmt.Errorf("replay balance changed")
			}
			if _, err = u.ByKey(ctx, "provider-b", b.Input.IdempotencyKey); !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("provider isolation failed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		assertWallet(w, 5000, 3)
	})
	t.Run("refund rollback compensation history and pending recovery", func(t *testing.T) {
		w := newWallet(t, 10000)
		open(w)
		apply := func(tr tx.Transaction) tx.State {
			t.Helper()
			entry, m := id(t), meta(t)
			var d processing.Decision
			if err := store.Within(ctx, func(u *Unit) error { var err error; d, err = execute(ctx, u, tr, entry, m); return err }); err != nil {
				t.Fatal(err)
			}
			s, _ := d.Transaction.Snapshot()
			return s
		}
		bet := apply(operation(t, w, tx.Bet, 8000, ""))
		refund := apply(operation(t, w, tx.Refund, 8000, bet.Input.ExternalTransactionID))
		if apply(operation(t, w, tx.Rollback, 8000, refund.Input.ExternalTransactionID)).Status != tx.Processed {
			t.Fatal("refund rollback failed")
		}
		if apply(operation(t, w, tx.Refund, 8000, bet.Input.ExternalTransactionID)).FailureCode != tx.AlreadyReversed {
			t.Fatal("BET compensation reopened")
		}
		loss := apply(operation(t, w, tx.Loss, 0, ""))
		if loss.Result.WalletVersion != 4 {
			t.Fatal("LOSS changed version")
		}
		assertWallet(w, 2000, 4)
		missing := apply(operation(t, w, tx.Refund, 100, "not-arrived"))
		if missing.Status != tx.PendingReference {
			t.Fatal(missing.Status)
		}
		if count("SELECT count(*) FROM wagering.transaction_work WHERE transaction_id=$1", missing.Input.ID) != 1 {
			t.Fatal("pending work lost")
		}
		anotherPool, err := pgxpool.New(ctx, appURL)
		if err != nil {
			t.Fatal(err)
		}
		defer anotherPool.Close()
		if err = New(anotherPool).Within(ctx, func(u *Unit) error {
			tr, err := u.Transaction(ctx, missing.Input.ID)
			if err != nil {
				return err
			}
			s, _ := tr.Snapshot()
			if s.Status != tx.PendingReference {
				return fmt.Errorf("pending state lost")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reconciliation and down up", func(t *testing.T) {
		if count(`SELECT count(*) FROM wagering.wallets w WHERE w.balance_minor::numeric<>coalesce((SELECT sum(CASE direction WHEN 'CREDIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END) FROM wagering.wallet_ledger_entries e WHERE e.wallet_id=w.id),0)`) != 0 {
			t.Fatal("ledger reconciliation failed")
		}
		if count(`SELECT count(*) FROM (SELECT transaction_id FROM wagering.postings GROUP BY transaction_id HAVING count(*)<>2 OR sum(CASE side WHEN 'DEBIT' THEN amount_minor::numeric ELSE -amount_minor::numeric END)<>0) x`) != 0 {
			t.Fatal("journal reconciliation failed")
		}
		if err := migrations.Run(ctx, migrationURL, 0); err != nil {
			t.Fatal(err)
		}
		if err := migrations.Run(ctx, migrationURL, migrations.Latest()); err != nil {
			t.Fatal(err)
		}
		if count("SELECT count(*) FROM wagering.wallets") != 0 {
			t.Fatal("reapply not clean")
		}
		conn, err := pgx.Connect(ctx, migrationURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var originalChecksum string
		if err = conn.QueryRow(ctx, "SELECT checksum FROM wagering.schema_migrations WHERE version=1").Scan(&originalChecksum); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, "UPDATE wagering.schema_migrations SET checksum=$1 WHERE version=1", strings.Repeat("0", 64)); err != nil {
			t.Fatal(err)
		}
		if err = migrations.Run(ctx, migrationURL, migrations.Latest()); err == nil {
			t.Fatal("modified migration checksum accepted")
		}
		if _, err = conn.Exec(ctx, "UPDATE wagering.schema_migrations SET checksum=$1 WHERE version=1", originalChecksum); err != nil {
			t.Fatal(err)
		}
	})
}
func snapshotID(tr tx.Transaction) string { s, _ := tr.Snapshot(); return s.Input.ID }
