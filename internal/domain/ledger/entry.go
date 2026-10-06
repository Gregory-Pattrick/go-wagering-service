// Package ledger models immutable wallet ledger entries.
package ledger

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

var (
	ErrInvalidIdentity   = errors.New("ledger identities must be nonzero canonical UUIDs")
	ErrInvalidDirection  = errors.New("invalid ledger direction")
	ErrNonPositiveAmount = errors.New("ledger amount must be positive")
	ErrNegativeBalance   = errors.New("ledger balances must be nonnegative")
	ErrBalanceMismatch   = errors.New("ledger balance equation does not match")
	ErrInvalidTime       = errors.New("ledger creation time is required")
)

// State is a detached snapshot for persistence. Every Money value is immutable.
type State struct {
	ID            string
	WalletID      string
	TransactionID string
	Direction     Direction
	Money         money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	CreatedAt     time.Time
}

// Entry exposes no mutation methods. Corrections require a separate transaction
// and a new compensating entry; an existing entry must never be rewritten.
type Entry struct{ state State }

// New validates the complete balance equation before accepting an entry.
// The application must also verify the transaction's kind, status and ownership
// and persist the entry atomically with its wallet and transaction changes.
func New(state State) (Entry, error) {
	return validated(state)
}

// Rehydrate restores a persisted entry without applying a financial movement.
// Persisted data is subject to the same validation as new entries.
func Rehydrate(state State) (Entry, error) {
	return validated(state)
}

func validUUID(id string) bool {
	if len(id) != 36 || id != strings.ToLower(id) || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
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

func validated(state State) (Entry, error) {
	for _, id := range []string{state.ID, state.WalletID, state.TransactionID} {
		if !validUUID(id) {
			return Entry{}, ErrInvalidIdentity
		}
	}
	if state.Direction != Debit && state.Direction != Credit {
		return Entry{}, ErrInvalidDirection
	}
	sign, err := state.Money.Sign()
	if err != nil {
		return Entry{}, err
	}
	if sign <= 0 {
		return Entry{}, ErrNonPositiveAmount
	}
	for _, balance := range []money.Money{state.BalanceBefore, state.BalanceAfter} {
		sign, err := balance.Sign()
		if err != nil {
			return Entry{}, err
		}
		if sign < 0 {
			return Entry{}, ErrNegativeBalance
		}
		if _, err := state.Money.Compare(balance); err != nil {
			return Entry{}, err
		}
	}
	if state.CreatedAt.IsZero() {
		return Entry{}, ErrInvalidTime
	}
	var expected money.Money
	if state.Direction == Credit {
		expected, err = state.BalanceBefore.Add(state.Money)
	} else {
		expected, err = state.BalanceBefore.Subtract(state.Money)
	}
	if err != nil {
		return Entry{}, err
	}
	sign, err = expected.Sign()
	if err != nil {
		return Entry{}, err
	}
	if sign < 0 {
		return Entry{}, ErrNegativeBalance
	}
	comparison, err := expected.Compare(state.BalanceAfter)
	if err != nil {
		return Entry{}, err
	}
	if comparison != 0 {
		return Entry{}, ErrBalanceMismatch
	}
	state.CreatedAt = state.CreatedAt.UTC()
	return Entry{state: state}, nil
}

func (e Entry) Validate() error {
	_, err := validated(e.state)
	return err
}

func (e Entry) Snapshot() (State, error) {
	if err := e.Validate(); err != nil {
		return State{}, err
	}
	return e.state, nil
}
