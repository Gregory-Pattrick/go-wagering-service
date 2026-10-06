// Package processing evaluates external wagering rules over validated snapshots.
// It performs no I/O and does not provide distributed locking or persistence.
package processing

import (
	"errors"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

var (
	ErrTerminalTransaction    = errors.New("terminal transaction must be replayed, not evaluated")
	ErrInternalOperation      = errors.New("opening requires the internal wallet creation flow")
	ErrUnexpectedReference    = errors.New("reference supplied for an operation without a reference")
	ErrInvalidReferenceRecord = errors.New("inconsistent persisted reference transaction or ledger")
)

// Reference must come from a provider-scoped database lookup in the same SQL
// transaction as the locked wallet. Compensated means a successful REFUND or
// ROLLBACK already targets this transaction. Rolling back a REFUND must not erase
// the original BET's compensation history.
type Reference struct {
	Transaction tx.Transaction
	Ledger      *ledger.Entry
	Compensated bool
}

type Request struct {
	Transaction tx.Transaction
	Wallet      wallet.Wallet
	Reference   *Reference
	LedgerID    string
	Now         time.Time
}

// Decision is a proposal to commit atomically, not proof that anything was saved.
// Entry is nil for LOSS, rejected operations and pending references.
type Decision struct {
	Transaction tx.Transaction
	Wallet      wallet.Wallet
	Entry       *ledger.Entry
}

func Evaluate(request Request) (Decision, error) {
	operation, err := request.Transaction.Snapshot()
	if err != nil {
		return Decision{}, err
	}
	if operation.Status != tx.Pending && operation.Status != tx.PendingReference {
		return Decision{}, ErrTerminalTransaction
	}
	if operation.Input.Kind == tx.Opening {
		return Decision{}, ErrInternalOperation
	}
	current, err := request.Wallet.Snapshot()
	if err != nil {
		return Decision{}, err
	}
	if request.Now.IsZero() {
		return Decision{}, tx.ErrInvalidTime
	}
	// Use one occurrence time for the resulting transaction, wallet and entry.
	now := request.Now.UTC()
	if now.Before(current.UpdatedAt) {
		now = current.UpdatedAt
	}
	if now.Before(operation.UpdatedAt) {
		now = operation.UpdatedAt
	}
	decision := Decision{Transaction: request.Transaction, Wallet: request.Wallet}
	reject := func(code tx.FailureCode, referenceID string, includeBalance bool) (Decision, error) {
		var result *tx.Result
		if includeBalance {
			result = &tx.Result{Balance: current.Balance, WalletVersion: current.Version}
		}
		rejected, err := request.Transaction.Reject(code, result, referenceID, now)
		if err != nil {
			return Decision{}, err
		}
		decision.Transaction = rejected
		return decision, nil
	}
	if current.ID != operation.Input.WalletID || current.PlayerID != operation.Input.PlayerID {
		return reject(tx.WalletMismatch, "", false)
	}
	if _, err := current.Balance.Compare(operation.Input.Money); err != nil {
		if errors.Is(err, money.ErrCurrencyMismatch) {
			return reject(tx.WalletMismatch, "", false)
		}
		return Decision{}, err
	}
	direction := ledger.Credit
	if operation.Input.Kind == tx.Bet {
		direction = ledger.Debit
	}
	referenceID := ""
	if operation.Input.ReferenceExternalTransactionID == "" {
		if request.Reference != nil {
			return Decision{}, ErrUnexpectedReference
		}
	} else {
		wait := func() (Decision, error) {
			if operation.Status == tx.PendingReference {
				return decision, nil
			}
			waiting, err := request.Transaction.WaitForReference(now)
			if err != nil {
				return Decision{}, err
			}
			decision.Transaction = waiting
			return decision, nil
		}
		if request.Reference == nil {
			return wait()
		}
		original, err := request.Reference.Transaction.Snapshot()
		if err != nil {
			return Decision{}, ErrInvalidReferenceRecord
		}
		// Provider/external identity is checked even though the repository must scope it.
		if original.Input.ProviderID != operation.Input.ProviderID ||
			original.Input.ExternalTransactionID != operation.Input.ReferenceExternalTransactionID ||
			original.Input.PlayerID != operation.Input.PlayerID || original.Input.WalletID != operation.Input.WalletID ||
			original.Input.RoundID != operation.Input.RoundID || original.Input.ID == operation.Input.ID {
			return reject(tx.ReferenceMismatch, "", true)
		}
		comparison, err := operation.Input.Money.Compare(original.Input.Money)
		if err != nil {
			return reject(tx.ReferenceMismatch, "", true)
		}
		kindAllowed := original.Input.Kind == tx.Bet
		if operation.Input.Kind == tx.Rollback {
			kindAllowed = original.Input.Kind == tx.Bet || original.Input.Kind == tx.Win || original.Input.Kind == tx.Refund
		}
		if !kindAllowed {
			return reject(tx.ReferenceKindInvalid, "", true)
		}
		if (operation.Input.Kind == tx.Refund || operation.Input.Kind == tx.Rollback) && comparison != 0 {
			return reject(tx.ReferenceMismatch, "", true)
		}
		referenceID = original.Input.ID
		switch original.Status {
		case tx.Pending, tx.PendingReference:
			return wait()
		case tx.Rejected, tx.Failed:
			return reject(tx.ReferenceNotProcessed, referenceID, true)
		case tx.Processed:
		default:
			return Decision{}, ErrInvalidReferenceRecord
		}
		if request.Reference.Ledger == nil {
			return Decision{}, ErrInvalidReferenceRecord
		}
		entry, err := request.Reference.Ledger.Snapshot()
		if err != nil {
			return Decision{}, ErrInvalidReferenceRecord
		}
		expectedDirection := ledger.Credit
		if original.Input.Kind == tx.Bet {
			expectedDirection = ledger.Debit
		}
		if entry.WalletID != original.Input.WalletID || entry.TransactionID != original.Input.ID || entry.Direction != expectedDirection {
			return Decision{}, ErrInvalidReferenceRecord
		}
		equal, err := entry.Money.Compare(original.Input.Money)
		if err != nil || equal != 0 {
			return Decision{}, ErrInvalidReferenceRecord
		}
		equal, err = entry.BalanceAfter.Compare(original.Result.Balance)
		if err != nil || equal != 0 {
			return Decision{}, ErrInvalidReferenceRecord
		}
		if operation.Input.Kind == tx.Refund || operation.Input.Kind == tx.Rollback {
			if request.Reference.Compensated {
				return reject(tx.AlreadyReversed, referenceID, true)
			}
			if operation.Input.Kind == tx.Rollback {
				direction = ledger.Debit
				if entry.Direction == ledger.Debit {
					direction = ledger.Credit
				}
			}
		}
	}
	if operation.Input.Kind == tx.Loss {
		completed, err := request.Transaction.MarkProcessed(tx.Result{Balance: current.Balance, WalletVersion: current.Version}, "", now)
		if err != nil {
			return Decision{}, err
		}
		decision.Transaction = completed
		return decision, nil
	}
	var updated wallet.Wallet
	if direction == ledger.Debit {
		updated, err = request.Wallet.Debit(operation.Input.Money, now)
	} else {
		updated, err = request.Wallet.Credit(operation.Input.Money, now)
	}
	if err != nil {
		if errors.Is(err, wallet.ErrInsufficientFunds) {
			code := tx.InsufficientFunds
			if operation.Input.Kind == tx.Rollback {
				code = tx.ReversalInsufficientFunds
			}
			return reject(code, referenceID, true)
		}
		if errors.Is(err, money.ErrOverflow) {
			return reject(tx.BalanceOverflow, referenceID, true)
		}
		return Decision{}, err
	}
	after, err := updated.Snapshot()
	if err != nil {
		return Decision{}, err
	}
	entry, err := ledger.New(ledger.State{ID: request.LedgerID, WalletID: current.ID, TransactionID: operation.Input.ID,
		Direction: direction, Money: operation.Input.Money, BalanceBefore: current.Balance, BalanceAfter: after.Balance, CreatedAt: now})
	if err != nil {
		return Decision{}, err
	}
	completed, err := request.Transaction.MarkProcessed(tx.Result{Balance: after.Balance, WalletVersion: after.Version}, referenceID, now)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Transaction: completed, Wallet: updated, Entry: &entry}, nil
}
