// Package finance implements explicit SQL repositories inside one pgx transaction.
package finance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConflict      = errors.New("persistent financial identity conflict")
	ErrStaleWallet   = errors.New("wallet version changed")
	ErrCommitUnknown = errors.New("commit outcome unknown; retry the same business identity")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Unit struct {
	tx     pgx.Tx
	locked map[string]int64
}

// Within rolls back on callback errors and panics. It does not automatically
// retry callbacks or publish events. Unknown commit outcomes require idempotent
// identity lookup, never an unconditional second financial operation.
func (s *Store) Within(ctx context.Context, fn func(*Unit) error) error {
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = transaction.Rollback(cleanup)
	}()
	unit := &Unit{tx: transaction, locked: make(map[string]int64)}
	if err = fn(unit); err != nil {
		return err
	}
	if err = transaction.Commit(ctx); err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) || errors.Is(err, pgx.ErrTxCommitRollback) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrCommitUnknown, err)
	}
	return nil
}
func Retryable(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && (pgError.Code == "40001" || pgError.Code == "40P01")
}
func (u *Unit) InsertWallet(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return err
	}
	if err = checkTime(s.CreatedAt, s.UpdatedAt); err != nil {
		return err
	}
	if s.Version != 1 {
		return ErrStaleWallet
	}
	minor, _ := s.Balance.MinorUnits()
	currency, _ := s.Balance.Currency()
	_, err = u.tx.Exec(ctx, `INSERT INTO wagering.wallets(id,player_id,currency,opening_balance_minor,balance_minor,version,created_at,updated_at) VALUES($1,$2,$3,$4,$4,1,$5,$6)`, s.ID, s.PlayerID, string(currency), minor, s.CreatedAt, s.UpdatedAt)
	if err == nil {
		u.locked[s.ID] = 1
	}
	return err
}
func scanWallet(row pgx.Row) (wallet.Wallet, error) {
	var s wallet.State
	var minor int64
	var currency string
	if err := row.Scan(&s.ID, &s.PlayerID, &currency, &minor, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return wallet.Wallet{}, err
	}
	balance, err := money.FromMinor(minor, money.Currency(currency))
	if err != nil {
		return wallet.Wallet{}, err
	}
	s.Balance = balance
	return wallet.Rehydrate(s)
}
func (u *Unit) LockWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	w, err := scanWallet(u.tx.QueryRow(ctx, `SELECT id::text,player_id::text,currency,balance_minor,version,created_at,updated_at FROM wagering.wallets WHERE id=$1 FOR UPDATE`, id))
	if err == nil {
		s, _ := w.Snapshot()
		u.locked[s.ID] = s.Version
	}
	return w, err
}
func (s *Store) Wallet(ctx context.Context, id string) (wallet.Wallet, error) {
	return scanWallet(s.pool.QueryRow(ctx, `SELECT id::text,player_id::text,currency,balance_minor,version,created_at,updated_at FROM wagering.wallets WHERE id=$1`, id))
}
func (u *Unit) SaveWallet(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return err
	}
	version, ok := u.locked[s.ID]
	if !ok || s.Version != version+1 {
		return ErrStaleWallet
	}
	minor, _ := s.Balance.MinorUnits()
	result, err := u.tx.Exec(ctx, `UPDATE wagering.wallets SET balance_minor=$1,version=$2,updated_at=$3 WHERE id=$4 AND version=$5`, minor, s.Version, s.UpdatedAt, s.ID, version)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrStaleWallet
	}
	u.locked[s.ID] = s.Version
	return nil
}

// PostgreSQL timestamps have microsecond precision. Financial callers must use
// UTC time truncated to a microsecond before constructing immutable snapshots.
func checkTime(values ...time.Time) error {
	for _, value := range values {
		if !value.Equal(value.Truncate(time.Microsecond)) {
			return fmt.Errorf("financial timestamp must have microsecond precision")
		}
	}
	return nil
}
