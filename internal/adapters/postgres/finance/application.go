package finance

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ financial.Backend = (*Store)(nil)

func applicationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return financial.ErrNotFound
	}
	if errors.Is(err, ErrConflict) {
		return financial.ErrIdentityTaken
	}
	if errors.Is(err, ErrCommitUnknown) {
		return fmt.Errorf("%w: %v", financial.ErrOutcomeUnknown, err)
	}
	if Retryable(err) {
		return fmt.Errorf("%w: %v", financial.ErrRetryable, err)
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return financial.ErrUnique
	}
	return err
}

type applicationUnit struct{ *Unit }

func (u applicationUnit) InsertTransaction(ctx context.Context, tr tx.Transaction) error {
	return applicationError(u.Unit.InsertTransaction(ctx, tr))
}
func (u applicationUnit) ByKey(ctx context.Context, p, key string) (tx.Transaction, error) {
	tr, err := u.Unit.ByKey(ctx, p, key)
	return tr, applicationError(err)
}
func (u applicationUnit) ByExternal(ctx context.Context, p, external string) (tx.Transaction, error) {
	tr, err := u.Unit.ByExternal(ctx, p, external)
	return tr, applicationError(err)
}
func (u applicationUnit) LockWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	w, err := u.Unit.LockWallet(ctx, id)
	return w, applicationError(err)
}
func (u applicationUnit) SaveDecision(ctx context.Context, before tx.Transaction, d processing.Decision, m events.Metadata, s *financial.Schedule) error {
	var schedule *Schedule
	if s != nil {
		schedule = &Schedule{NextAttemptAt: s.NextAttemptAt, ExpiresAt: s.ExpiresAt}
	}
	return u.Unit.SaveDecision(ctx, before, d, m, schedule)
}
func (s *Store) WithinFinancial(ctx context.Context, fn func(financial.Unit) error) error {
	return applicationError(s.Within(ctx, func(u *Unit) error { return fn(applicationUnit{u}) }))
}
func (s *Store) ReadWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	w, err := s.Wallet(ctx, id)
	return w, applicationError(err)
}
func (s *Store) ReadTransaction(ctx context.Context, provider, id string) (tx.Transaction, error) {
	tr, err := scanTransaction(s.pool.QueryRow(ctx, "SELECT "+transactionColumns+" FROM wagering.wager_transactions WHERE provider_id=$1 AND id=$2", provider, id))
	return tr, applicationError(err)
}
func (s *Store) ReadExternal(ctx context.Context, provider, external string) (tx.Transaction, error) {
	tr, err := scanTransaction(s.pool.QueryRow(ctx, "SELECT "+transactionColumns+" FROM wagering.wager_transactions WHERE provider_id=$1 AND external_id=$2", provider, external))
	return tr, applicationError(err)
}
func (s *Store) ReadLedger(ctx context.Context, id string, c financial.Cursor, limit int) (financial.LedgerPage, error) {
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return financial.LedgerPage{}, err
	}
	defer transaction.Rollback(ctx)
	var version int64
	if err = transaction.QueryRow(ctx, "SELECT version FROM wagering.wallets WHERE id=$1", id).Scan(&version); err != nil {
		return financial.LedgerPage{}, applicationError(err)
	}
	if c.Through == 0 {
		c.Through = version
	}
	if c.Through > version {
		return financial.LedgerPage{}, financial.Failure("INVALID_CURSOR")
	}
	rows, err := transaction.Query(ctx, `SELECT id::text,wallet_id::text,transaction_id::text,direction,amount_minor,currency,balance_before,balance_after,wallet_version,created_at FROM wagering.wallet_ledger_entries WHERE wallet_id=$1 AND wallet_version>$2 AND wallet_version<=$3 ORDER BY wallet_version,id LIMIT $4`, id, c.After, c.Through, limit+1)
	if err != nil {
		return financial.LedgerPage{}, err
	}
	page := financial.LedgerPage{Items: make([]financial.LedgerItem, 0)}
	for rows.Next() {
		var item financial.LedgerItem
		var direction, currency string
		var amount, before, after int64
		if err = rows.Scan(&item.ID, &item.WalletID, &item.TransactionID, &direction, &amount, &currency, &before, &after, &item.WalletVersion, &item.CreatedAt); err != nil {
			rows.Close()
			return page, err
		}
		item.Direction = ledger.Direction(direction)
		item.CreatedAt = item.CreatedAt.UTC()
		item.Money, err = money.FromMinor(amount, money.Currency(currency))
		if err != nil {
			rows.Close()
			return page, err
		}
		item.BalanceBefore, err = money.FromMinor(before, money.Currency(currency))
		if err != nil {
			rows.Close()
			return page, err
		}
		item.BalanceAfter, err = money.FromMinor(after, money.Currency(currency))
		if err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = financial.Cursor{After: page.Items[limit-1].WalletVersion, Through: c.Through}.Encode(id)
	}
	return page, transaction.Commit(ctx)
}
func (s *Store) Reconcile(ctx context.Context, id string) (financial.Reconciliation, error) {
	// One SELECT gives wallet and ledger a single MVCC snapshot even under writes.
	var currency, total string
	var stored, count int64
	err := s.pool.QueryRow(ctx, `SELECT w.currency,w.balance_minor,coalesce(sum(CASE e.direction WHEN 'CREDIT' THEN e.amount_minor::numeric ELSE -e.amount_minor::numeric END),0)::text,count(e.id) FROM wagering.wallets w LEFT JOIN wagering.wallet_ledger_entries e ON e.wallet_id=w.id WHERE w.id=$1 GROUP BY w.id`, id).Scan(&currency, &stored, &total, &count)
	if err != nil {
		return financial.Reconciliation{}, applicationError(err)
	}
	calculated, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return financial.Reconciliation{}, fmt.Errorf("reconciliation total outside supported Money range")
	}
	storedMoney, err := money.FromMinor(stored, money.Currency(currency))
	if err != nil {
		return financial.Reconciliation{}, err
	}
	calculatedMoney, err := money.FromMinor(calculated, money.Currency(currency))
	if err != nil {
		return financial.Reconciliation{}, err
	}
	difference, err := storedMoney.Subtract(calculatedMoney)
	if err != nil {
		return financial.Reconciliation{}, err
	}
	return financial.Reconciliation{WalletID: id, StoredBalance: storedMoney, CalculatedBalance: calculatedMoney, Difference: difference, Consistent: stored == calculated, CheckedEntries: count}, nil
}
