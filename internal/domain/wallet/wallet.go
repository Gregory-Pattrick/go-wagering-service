// Package wallet implements the financial wallet aggregate without infrastructure.
package wallet

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
)

var (
	ErrInvalidID         = errors.New("wallet and player IDs must be nonzero canonical UUIDs")
	ErrInvalidBalance    = errors.New("wallet balance must be nonnegative")
	ErrInvalidVersion    = errors.New("wallet version must be positive")
	ErrVersionOverflow   = errors.New("wallet version exceeds int64 range")
	ErrInvalidTime       = errors.New("invalid wallet timestamps")
	ErrNonPositiveAmount = errors.New("wallet movement must be positive")
	ErrInsufficientFunds = errors.New("insufficient wallet balance")
)

// State is a detached persistence snapshot. Editing it never changes a Wallet.
// Rehydrate validates every snapshot before accepting it as an aggregate.
type State struct {
	ID        string
	PlayerID  string
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Wallet keeps its state private. Credit and Debit return a new aggregate;
// callers must retain the returned value and persist it inside their SQL unit of work.
type Wallet struct{ state State }

// New accepts zero or positive opening balances. Version always starts at one.
// Opening transaction, ledger and outbox records are the application layer's
// responsibility and must be committed atomically with this wallet.
func New(id, playerID string, initial money.Money, now time.Time) (Wallet, error) {
	return Rehydrate(State{ID: id, PlayerID: playerID, Balance: initial,
		Version: 1, CreatedAt: now, UpdatedAt: now})
}

// Rehydrate restores persisted state without generating movements or events.
func Rehydrate(state State) (Wallet, error) {
	if !validID(state.ID) || !validID(state.PlayerID) {
		return Wallet{}, ErrInvalidID
	}
	sign, err := state.Balance.Sign()
	if err != nil {
		return Wallet{}, err
	}
	if sign < 0 {
		return Wallet{}, ErrInvalidBalance
	}
	if state.Version < 1 {
		return Wallet{}, ErrInvalidVersion
	}
	if state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() || state.UpdatedAt.Before(state.CreatedAt) {
		return Wallet{}, ErrInvalidTime
	}
	state.CreatedAt = state.CreatedAt.UTC()
	state.UpdatedAt = state.UpdatedAt.UTC()
	return Wallet{state: state}, nil
}

func validID(id string) bool {
	if len(id) != 36 || id != strings.ToLower(id) ||
		id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(compact)
	if err != nil {
		return false
	}
	for _, b := range decoded {
		if b != 0 {
			return true
		}
	}
	return false
}

func (w Wallet) Validate() error {
	_, err := Rehydrate(w.state)
	return err
}

func (w Wallet) Snapshot() (State, error) {
	if err := w.Validate(); err != nil {
		return State{}, err
	}
	return w.state, nil
}

func (w Wallet) Credit(amount money.Money, now time.Time) (Wallet, error) {
	return w.move(amount, now, false)
}

func (w Wallet) Debit(amount money.Money, now time.Time) (Wallet, error) {
	return w.move(amount, now, true)
}

func (w Wallet) move(amount money.Money, now time.Time, debit bool) (Wallet, error) {
	if err := w.Validate(); err != nil {
		return Wallet{}, err
	}
	comparison, err := w.state.Balance.Compare(amount)
	if err != nil {
		return Wallet{}, err
	}
	sign, err := amount.Sign()
	if err != nil {
		return Wallet{}, err
	}
	if sign <= 0 {
		return Wallet{}, ErrNonPositiveAmount
	}
	if now.IsZero() {
		return Wallet{}, ErrInvalidTime
	}
	if debit && comparison < 0 {
		return Wallet{}, ErrInsufficientFunds
	}
	if w.state.Version == math.MaxInt64 {
		return Wallet{}, ErrVersionOverflow
	}
	var balance money.Money
	if debit {
		balance, err = w.state.Balance.Subtract(amount)
	} else {
		balance, err = w.state.Balance.Add(amount)
	}
	if err != nil {
		return Wallet{}, err
	}
	next := w.state
	next.Balance = balance
	next.Version++
	// Preserve timestamp ordering if application clocks move backwards.
	next.UpdatedAt = now.UTC()
	if next.UpdatedAt.Before(next.CreatedAt) || next.UpdatedAt.Before(w.state.UpdatedAt) {
		next.UpdatedAt = w.state.UpdatedAt
	}
	return Rehydrate(next)
}
