package finance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
	"github.com/jackc/pgx/v5"
)

type Schedule struct{ NextAttemptAt, ExpiresAt time.Time }

func (u *Unit) Schedule(ctx context.Context, id string, s Schedule) error {
	if s.NextAttemptAt.IsZero() || s.ExpiresAt.Before(s.NextAttemptAt) {
		return fmt.Errorf("invalid durable schedule")
	}
	_, err := u.tx.Exec(ctx, `INSERT INTO wagering.transaction_work(transaction_id,next_attempt_at,expires_at) VALUES($1,$2,$3) ON CONFLICT(transaction_id) DO NOTHING`, id, s.NextAttemptAt, s.ExpiresAt)
	return err
}
func (u *Unit) AppendEntry(ctx context.Context, entry ledger.Entry, version int64) error {
	e, err := entry.Snapshot()
	if err != nil {
		return err
	}
	if u.locked[e.WalletID] != version {
		return ErrStaleWallet
	}
	amount, _ := e.Money.MinorUnits()
	currency, _ := e.Money.Currency()
	before, _ := e.BalanceBefore.MinorUnits()
	after, _ := e.BalanceAfter.MinorUnits()
	_, err = u.tx.Exec(ctx, `INSERT INTO wagering.wallet_ledger_entries(id,wallet_id,transaction_id,direction,amount_minor,currency,balance_before,balance_after,wallet_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, e.ID, e.WalletID, e.TransactionID, string(e.Direction), amount, string(currency), before, after, version, e.CreatedAt)
	return err
}
func (u *Unit) AppendJournal(ctx context.Context, entry ledger.Entry, kind tx.Kind) error {
	e, err := entry.Snapshot()
	if err != nil {
		return err
	}
	amount, _ := e.Money.MinorUnits()
	currency, _ := e.Money.Currency()
	account := "wallet:" + e.WalletID
	if _, err = u.tx.Exec(ctx, `INSERT INTO wagering.accounts(id,currency,role,wallet_id) VALUES($1,$2,'WALLET_LIABILITY',$3) ON CONFLICT(wallet_id) WHERE wallet_id IS NOT NULL DO NOTHING`, account, string(currency), e.WalletID); err != nil {
		return err
	}
	if _, err = u.tx.Exec(ctx, `INSERT INTO wagering.journals(transaction_id,ledger_id,created_at) VALUES($1,$2,$3)`, e.TransactionID, e.ID, e.CreatedAt); err != nil {
		return err
	}
	clearing := "game:" + string(currency)
	if kind == tx.Opening {
		clearing = "opening:" + string(currency)
	}
	other := ledger.Debit
	if e.Direction == ledger.Debit {
		other = ledger.Credit
	}
	_, err = u.tx.Exec(ctx, `INSERT INTO wagering.postings(transaction_id,side,account_id,amount_minor,currency) VALUES($1,$2,$3,$4,$5),($1,$6,$7,$4,$5)`, e.TransactionID, string(e.Direction), account, amount, string(currency), string(other), clearing)
	return err
}
func (u *Unit) AddEvents(ctx context.Context, transactionID string, batch []events.Event) error {
	for _, event := range batch {
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		var header struct {
			OccurredAt time.Time `json:"occurredAt"`
		}
		if err = json.Unmarshal(payload, &header); err != nil {
			return err
		}
		if _, err = u.tx.Exec(ctx, `INSERT INTO wagering.outbox(event_id,transaction_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) VALUES($1,$2,$3,$4,$5::jsonb,$6,$6)`, event.ID(), transactionID, event.Type(), event.AggregateID(), string(payload), header.OccurredAt); err != nil {
			return err
		}
	}
	return nil
}

// SaveDecision writes a complete proposal; only Store.Within commits it.
func (u *Unit) SaveDecision(ctx context.Context, before tx.Transaction, d processing.Decision, metadata events.Metadata, schedule *Schedule) error {
	old, err := before.Snapshot()
	if err != nil {
		return err
	}
	next, err := d.Transaction.Snapshot()
	if err != nil {
		return err
	}
	batch, err := events.ForTransition(before, d.Transaction, d.Entry, metadata)
	if err != nil {
		return err
	}
	if d.Entry != nil {
		w, err := d.Wallet.Snapshot()
		if err != nil {
			return err
		}
		if w.Version != u.locked[w.ID] {
			if err = u.SaveWallet(ctx, d.Wallet); err != nil {
				return err
			}
		}
	}
	if old.Status != next.Status {
		if err = u.SaveTransaction(ctx, before, d.Transaction); err != nil {
			return err
		}
	}
	if d.Entry != nil {
		if err = u.AppendEntry(ctx, *d.Entry, next.Result.WalletVersion); err != nil {
			return err
		}
		if err = u.AppendJournal(ctx, *d.Entry, next.Input.Kind); err != nil {
			return err
		}
	}
	if err = u.AddEvents(ctx, next.Input.ID, batch); err != nil {
		return err
	}
	if next.Status == tx.Pending || next.Status == tx.PendingReference {
		if schedule == nil {
			return fmt.Errorf("pending decision requires a durable schedule")
		}
		return u.Schedule(ctx, next.Input.ID, *schedule)
	}
	_, err = u.tx.Exec(ctx, "DELETE FROM wagering.transaction_work WHERE transaction_id=$1", next.Input.ID)
	return err
}
func (u *Unit) OpenWallet(ctx context.Context, w wallet.Wallet, openingID, entryID string, metadata events.Metadata) error {
	if err := u.InsertWallet(ctx, w); err != nil {
		return err
	}
	s, _ := w.Snapshot()
	sign, _ := s.Balance.Sign()
	if sign == 0 {
		return nil
	}
	pending, err := tx.NewOpening(tx.OpeningInput{ID: openingID, WalletID: s.ID, PlayerID: s.PlayerID, Money: s.Balance}, s.CreatedAt)
	if err != nil {
		return err
	}
	if err = u.InsertTransaction(ctx, pending); err != nil {
		return err
	}
	completed, err := pending.MarkProcessed(tx.Result{Balance: s.Balance, WalletVersion: 1}, "", s.CreatedAt)
	if err != nil {
		return err
	}
	currency, _ := s.Balance.Currency()
	zero, _ := money.Zero(currency)
	entry, err := ledger.New(ledger.State{ID: entryID, WalletID: s.ID, TransactionID: openingID, Direction: ledger.Credit, Money: s.Balance, BalanceBefore: zero, BalanceAfter: s.Balance, CreatedAt: s.CreatedAt})
	if err != nil {
		return err
	}
	return u.SaveDecision(ctx, pending, processing.Decision{Transaction: completed, Wallet: w, Entry: &entry}, metadata, nil)
}
func (u *Unit) Entry(ctx context.Context, id string) (ledger.Entry, error) {
	var s ledger.State
	var amount, before, after int64
	var currency, direction string
	err := u.tx.QueryRow(ctx, `SELECT id::text,wallet_id::text,transaction_id::text,direction,amount_minor,currency,balance_before,balance_after,created_at FROM wagering.wallet_ledger_entries WHERE transaction_id=$1`, id).Scan(&s.ID, &s.WalletID, &s.TransactionID, &direction, &amount, &currency, &before, &after, &s.CreatedAt)
	if err != nil {
		return ledger.Entry{}, err
	}
	s.Direction = ledger.Direction(direction)
	s.Money, err = money.FromMinor(amount, money.Currency(currency))
	if err != nil {
		return ledger.Entry{}, err
	}
	s.BalanceBefore, err = money.FromMinor(before, money.Currency(currency))
	if err != nil {
		return ledger.Entry{}, err
	}
	s.BalanceAfter, err = money.FromMinor(after, money.Currency(currency))
	if err != nil {
		return ledger.Entry{}, err
	}
	return ledger.Rehydrate(s)
}

// Reference must be called after LockWallet. This lock serializes every writer
// of the same wallet; immutable reference rows need no second row lock.
func (u *Unit) Reference(ctx context.Context, provider, external, walletID string) (*processing.Reference, error) {
	if _, ok := u.locked[walletID]; !ok {
		return nil, ErrStaleWallet
	}
	tr, err := u.ByExternal(ctx, provider, external)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state, _ := tr.Snapshot()
	ref := &processing.Reference{Transaction: tr}
	if state.Status == tx.Processed && state.Input.Kind != tx.Loss {
		entry, err := u.Entry(ctx, state.Input.ID)
		if err != nil {
			return nil, err
		}
		ref.Ledger = &entry
	}
	err = u.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wagering.wager_transactions WHERE resolved_reference_id=$1 AND status='PROCESSED' AND kind IN ('REFUND','ROLLBACK'))`, state.Input.ID).Scan(&ref.Compensated)
	return ref, err
}

// CompleteInbox is part of the financial unit of work, never a separate commit.
func (u *Unit) CompleteInbox(ctx context.Context, consumer, message, hash, transactionID string, received, completed time.Time) error {
	_, err := u.tx.Exec(ctx, `INSERT INTO wagering.inbox(consumer_name,message_id,payload_hash,transaction_id,received_at,completed_at) VALUES($1,$2,$3,$4,$5,$6)`, consumer, message, hash, transactionID, received, completed)
	return err
}
