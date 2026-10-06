//go:build financialintegration && inboxintegration

package finance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/adapters/postgres/migrations"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/jackc/pgx/v5/pgxpool"
)

type failingInbox struct {
	*Store
	failure error
}

func (s failingInbox) WithinInbox(ctx context.Context, identity financial.InboxIdentity, fn func(financial.Unit) error) error {
	return s.Store.WithinInbox(ctx, identity, func(u financial.Unit) error {
		if err := fn(u); err != nil {
			return err
		}
		return s.failure
	})
}
func TestInboxIntegration(t *testing.T) {
	migrationURL, appURL := os.Getenv("FINANCE_TEST_MIGRATION_URL"), os.Getenv("FINANCE_TEST_DATABASE_URL")
	for _, raw := range []string{migrationURL, appURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() != "finance-postgres" || u.Path != "/wagering" {
			t.Fatal("refusing reset outside finance-postgres/wagering")
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
	service := financial.New(func() (financial.Backend, error) { return store, nil })
	principal, _ := auth.NewProvider("trusted-producer", "provider-a")
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	prepare := func(kind tx.Kind, amount int64, reference string) (tx.ExternalInput, financial.InboxIdentity) {
		t.Helper()
		w := newWallet(t, 10000)
		if err := store.Within(ctx, func(u *Unit) error { return u.OpenWallet(ctx, w, id(t), id(t), meta(t)) }); err != nil {
			t.Fatal(err)
		}
		tr := operation(t, w, kind, amount, reference)
		state, _ := tr.Snapshot()
		input := state.Input
		identity := financial.InboxIdentity{Consumer: "wager-transactions-v1", MessageID: id(t), Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(input.ExternalTransactionID))), Provider: input.ProviderID, Key: input.IdempotencyKey}
		return input, identity
	}
	metadata := financial.Metadata{CorrelationID: "inbox-integration"}
	t.Run("cross-transport contention and original snapshot replay", func(t *testing.T) {
		input, identity := prepare(tx.Bet, 8000, "")
		start := make(chan struct{})
		responses := make(chan financial.TransactionResponse, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			r, err := service.Submit(ctx, principal, input, metadata)
			responses <- r
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			r, err := financial.SubmitInbox(ctx, store, principal, input, metadata, identity)
			responses <- r
			errs <- err
		}()
		close(start)
		wg.Wait()
		close(responses)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var transactionID string
		for response := range responses {
			if transactionID != "" && transactionID != response.TransactionID {
				t.Fatal("transports created different transactions")
			}
			transactionID = response.TransactionID
		}
		if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE transaction_id=$1", transactionID) != 1 || count("SELECT count(*) FROM wagering.inbox WHERE message_id=$1", identity.MessageID) != 1 {
			t.Fatal("duplicate debit or missing inbox")
		}
		win := input
		win.ExternalTransactionID = id(t)
		win.IdempotencyKey = id(t)
		win.Kind = tx.Win
		win.Money = cash(t, 3000)
		if _, err := service.Submit(ctx, principal, win, metadata); err != nil {
			t.Fatal(err)
		}
		// Same envelope, a new pool and a different SQS envelope all replay the BET's
		// original result even after a later WIN changed the current balance.
		restarted, err := pgxpool.New(ctx, appURL)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		for i := 0; i < 2; i++ {
			if i == 1 {
				identity.MessageID = id(t)
			}
			replay, err := financial.SubmitInbox(ctx, New(restarted), principal, input, metadata, identity)
			if err != nil {
				t.Fatal(err)
			}
			if replay.TransactionID != transactionID || !replay.IdempotentReplay {
				t.Fatal("not an original replay")
			}
			if replay.Balance == nil {
				t.Fatal("missing replay balance")
			}
			n, _ := replay.Balance.MinorUnits()
			if n != 2000 {
				t.Fatal("replay returned current balance")
			}
		}
		if count("SELECT count(*) FROM wagering.inbox WHERE transaction_id=$1", transactionID) != 2 {
			t.Fatal("new envelope was not durably recorded")
		}
		identity.Hash = fmt.Sprintf("%x", sha256.Sum256([]byte("different-envelope")))
		_, err = financial.SubmitInbox(ctx, store, principal, input, metadata, identity)
		var problem *financial.Error
		if !errors.As(err, &problem) || problem.Code != "INBOX_PAYLOAD_CONFLICT" {
			t.Fatal("inbox hash conflict accepted", err)
		}
	})
	t.Run("failure between financial writes and inbox completion rolls back everything", func(t *testing.T) {
		input, identity := prepare(tx.Bet, 8000, "")
		injected := errors.New("before inbox completion")
		_, err := financial.SubmitInbox(ctx, failingInbox{store, injected}, principal, input, metadata, identity)
		if !errors.Is(err, injected) {
			t.Fatal(err)
		}
		if count("SELECT count(*) FROM wagering.inbox WHERE message_id=$1", identity.MessageID) != 0 || count("SELECT count(*) FROM wagering.wager_transactions WHERE provider_id=$1 AND external_id=$2", input.ProviderID, input.ExternalTransactionID) != 0 {
			t.Fatal("partial inbox/transaction commit")
		}
		w, err := store.Wallet(ctx, input.WalletID)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := w.Snapshot()
		n, _ := s.Balance.MinorUnits()
		if n != 10000 || s.Version != 1 {
			t.Fatal("partial wallet commit")
		}
		if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE wallet_id=$1", input.WalletID) != 1 || count("SELECT count(*) FROM wagering.outbox WHERE aggregate_id=$1", input.WalletID) != 2 {
			t.Fatal("partial ledger/outbox commit")
		}
	})
	t.Run("pending references and business rejections complete inbox", func(t *testing.T) {
		for _, kind := range []tx.Kind{tx.Bet, tx.Refund} {
			amount := int64(20000)
			reference := ""
			want := "REJECTED"
			if kind == tx.Refund {
				amount = 100
				reference = id(t)
				want = "PENDING_REFERENCE"
			}
			input, identity := prepare(kind, amount, reference)
			response, err := financial.SubmitInbox(ctx, store, principal, input, metadata, identity)
			if err != nil {
				t.Fatal(err)
			}
			if string(response.Status) != want || count("SELECT count(*) FROM wagering.inbox WHERE message_id=$1", identity.MessageID) != 1 {
				t.Fatal("durable outcome missing")
			}
			if count("SELECT count(*) FROM wagering.wallet_ledger_entries WHERE transaction_id=$1", response.TransactionID) != 0 {
				t.Fatal("nonprocessed operation moved money")
			}
		}
	})
	t.Run("two deliveries with the same envelope commit one inbox row", func(t *testing.T) {
		input, identity := prepare(tx.Bet, 100, "")
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := financial.SubmitInbox(ctx, store, principal, input, metadata, identity)
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if count("SELECT count(*) FROM wagering.inbox WHERE message_id=$1", identity.MessageID) != 1 {
			t.Fatal("inbox not unique")
		}
	})
}
