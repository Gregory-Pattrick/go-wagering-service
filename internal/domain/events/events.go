// Package events constructs immutable, typed snapshots for the transactional outbox.
package events

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
)

const (
	ProcessedType        = "WagerTransactionProcessed"
	RejectedType         = "WagerTransactionRejected"
	BalanceChangedType   = "WalletBalanceChanged"
	PendingReferenceType = "WagerTransactionPendingReference"
)

var (
	ErrInvalidMetadata   = errors.New("invalid event identity or correlation metadata")
	ErrInvalidTransition = errors.New("invalid event-producing transaction transition")
	ErrInvalidLedger     = errors.New("event ledger does not match financial decision")
	ErrInvalidEvent      = errors.New("uninitialized event")
)

type Metadata struct {
	TransactionEventID string
	BalanceEventID     string
	CorrelationID      string
	CausationID        string
}

// Event exposes only metadata and JSON serialization; event fields cannot be set.
type Event interface {
	json.Marshaler
	ID() string
	Type() string
	AggregateID() string
}

type envelope[T any] struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          T         `json:"data"`
}
type record[T any] struct{ payload envelope[T] }

func (r record[T]) ID() string          { return r.payload.EventID }
func (r record[T]) Type() string        { return r.payload.EventType }
func (r record[T]) AggregateID() string { return r.payload.AggregateID }
func (r record[T]) MarshalJSON() ([]byte, error) {
	if r.payload.EventID == "" || r.payload.Version != 1 {
		return nil, ErrInvalidEvent
	}
	return json.Marshal(r.payload)
}

// ExternalData is absent for internal OPENING transactions.
type ExternalData struct {
	ProviderID                     string `json:"providerId"`
	ExternalTransactionID          string `json:"externalTransactionId"`
	IdempotencyKey                 string `json:"idempotencyKey"`
	PayloadHash                    string `json:"payloadHash"`
	RoundID                        string `json:"roundId"`
	GameID                         string `json:"gameId"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	ResolvedReferenceID            string `json:"resolvedReferenceId,omitempty"`
}
type TransactionData struct {
	TransactionID string        `json:"transactionId"`
	WalletID      string        `json:"walletId"`
	PlayerID      string        `json:"playerId"`
	Origin        string        `json:"origin"`
	Kind          tx.Kind       `json:"kind"`
	Money         money.Money   `json:"money"`
	External      *ExternalData `json:"external,omitempty"`
}
type FinancialResult struct {
	Balance       money.Money `json:"balance"`
	WalletVersion int64       `json:"walletVersion"`
}
type ProcessedData struct {
	TransactionData
	Result FinancialResult `json:"result"`
}
type RejectedData struct {
	TransactionData
	FailureCode tx.FailureCode   `json:"failureCode"`
	Result      *FinancialResult `json:"result,omitempty"`
}
type PendingReferenceData struct{ TransactionData }
type BalanceChangedData struct {
	WalletID      string           `json:"walletId"`
	TransactionID string           `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}

// These concrete types bind each event name to its data contract.
type WagerTransactionProcessed struct{ record[ProcessedData] }
type WagerTransactionRejected struct{ record[RejectedData] }
type WagerTransactionPendingReference struct{ record[PendingReferenceData] }
type WalletBalanceChanged struct{ record[BalanceChangedData] }

func validUUID(id string) bool {
	if len(id) != 36 || id != strings.ToLower(id) || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(id, "-", "")
	if len(compact) != 32 {
		return false
	}
	data, err := hex.DecodeString(compact)
	if err != nil {
		return false
	}
	for _, b := range data {
		if b != 0 {
			return true
		}
	}
	return false
}
func validText(s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func makeRecord[T any](id, eventType string, state tx.State, metadata Metadata, data T) (record[T], error) {
	if !validUUID(id) || !validText(metadata.CorrelationID) || (metadata.CausationID != "" && !validText(metadata.CausationID)) {
		return record[T]{}, ErrInvalidMetadata
	}
	return record[T]{payload: envelope[T]{EventID: id, EventType: eventType, AggregateID: state.Input.WalletID,
		CorrelationID: metadata.CorrelationID, CausationID: metadata.CausationID, OccurredAt: state.UpdatedAt.UTC(), Version: 1, Data: data}}, nil
}
func transactionData(state tx.State) TransactionData {
	input := state.Input
	data := TransactionData{TransactionID: input.ID, WalletID: input.WalletID, PlayerID: input.PlayerID, Origin: "EXTERNAL", Kind: input.Kind, Money: input.Money}
	if input.Kind == tx.Opening {
		data.Origin = "INTERNAL"
		return data
	}
	data.External = &ExternalData{ProviderID: input.ProviderID, ExternalTransactionID: input.ExternalTransactionID,
		IdempotencyKey: input.IdempotencyKey, PayloadHash: state.PayloadHash, RoundID: input.RoundID, GameID: input.GameID,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID, ResolvedReferenceID: state.ResolvedReferenceID}
	return data
}

// ForTransition is the constructor for event batches. Call it once for a new
// decision inside the financial unit of work, never for a persisted replay.
// Event IDs are supplied by the caller and must be persisted with these bytes.
func ForTransition(before, after tx.Transaction, entry *ledger.Entry, metadata Metadata) ([]Event, error) {
	previous, err := before.Snapshot()
	if err != nil {
		return nil, err
	}
	next, err := after.Snapshot()
	if err != nil {
		return nil, err
	}
	if previous.Status != tx.Pending && previous.Status != tx.PendingReference {
		return nil, ErrInvalidTransition
	}
	if previous.Input != next.Input || previous.PayloadHash != next.PayloadHash || !previous.CreatedAt.Equal(next.CreatedAt) || next.UpdatedAt.Before(previous.UpdatedAt) {
		return nil, ErrInvalidTransition
	}
	if previous.Status == next.Status {
		if entry != nil {
			return nil, ErrInvalidLedger
		}
		return nil, nil // Still waiting or not yet decided: no new logical event.
	}
	if next.Status == tx.Pending || (next.Status == tx.PendingReference && previous.Status != tx.Pending) {
		return nil, ErrInvalidTransition
	}
	data := transactionData(next)
	switch next.Status {
	case tx.Processed:
		if next.Input.Kind == tx.Loss {
			if entry != nil {
				return nil, ErrInvalidLedger
			}
		} else {
			if err := validateLedger(next, entry); err != nil {
				return nil, err
			}
			if metadata.TransactionEventID == metadata.BalanceEventID {
				return nil, ErrInvalidMetadata
			}
		}
		r, err := makeRecord(metadata.TransactionEventID, ProcessedType, next, metadata, ProcessedData{TransactionData: data,
			Result: FinancialResult{Balance: next.Result.Balance, WalletVersion: next.Result.WalletVersion}})
		if err != nil {
			return nil, err
		}
		batch := []Event{WagerTransactionProcessed{record: r}}
		if next.Input.Kind != tx.Loss {
			e, err := entry.Snapshot()
			if err != nil {
				return nil, err
			}
			balanceRecord, err := makeRecord(metadata.BalanceEventID, BalanceChangedType, next, metadata, BalanceChangedData{
				WalletID: e.WalletID, TransactionID: e.TransactionID, Direction: e.Direction, Money: e.Money,
				BalanceBefore: e.BalanceBefore, BalanceAfter: e.BalanceAfter, WalletVersion: next.Result.WalletVersion})
			if err != nil {
				return nil, err
			}
			batch = append(batch, WalletBalanceChanged{record: balanceRecord})
		}
		return batch, nil
	case tx.Rejected:
		if entry != nil {
			return nil, ErrInvalidLedger
		}
		payload := RejectedData{TransactionData: data, FailureCode: next.FailureCode}
		if next.Result != nil {
			payload.Result = &FinancialResult{Balance: next.Result.Balance, WalletVersion: next.Result.WalletVersion}
		}
		r, err := makeRecord(metadata.TransactionEventID, RejectedType, next, metadata, payload)
		if err != nil {
			return nil, err
		}
		return []Event{WagerTransactionRejected{record: r}}, nil
	case tx.PendingReference:
		if entry != nil {
			return nil, ErrInvalidLedger
		}
		r, err := makeRecord(metadata.TransactionEventID, PendingReferenceType, next, metadata, PendingReferenceData{TransactionData: data})
		if err != nil {
			return nil, err
		}
		return []Event{WagerTransactionPendingReference{record: r}}, nil
	case tx.Failed:
		if entry != nil {
			return nil, ErrInvalidLedger
		}
		return nil, nil // No FAILED event is specified by the challenge.
	default:
		return nil, ErrInvalidTransition
	}
}

func validateLedger(state tx.State, entry *ledger.Entry) error {
	if entry == nil {
		return ErrInvalidLedger
	}
	e, err := entry.Snapshot()
	if err != nil {
		return ErrInvalidLedger
	}
	if e.WalletID != state.Input.WalletID || e.TransactionID != state.Input.ID || !e.CreatedAt.Equal(state.UpdatedAt) {
		return ErrInvalidLedger
	}
	cmp, err := e.Money.Compare(state.Input.Money)
	if err != nil || cmp != 0 {
		return ErrInvalidLedger
	}
	cmp, err = e.BalanceAfter.Compare(state.Result.Balance)
	if err != nil || cmp != 0 {
		return ErrInvalidLedger
	}
	switch state.Input.Kind {
	case tx.Bet:
		if e.Direction != ledger.Debit {
			return ErrInvalidLedger
		}
	case tx.Win, tx.Refund, tx.Opening:
		if e.Direction != ledger.Credit {
			return ErrInvalidLedger
		}
	case tx.Rollback: // Direction was resolved against the original ledger by processing.
	default:
		return ErrInvalidLedger
	}
	if state.Input.Kind == tx.Opening {
		sign, err := e.BalanceBefore.Sign()
		if err != nil || sign != 0 {
			return ErrInvalidLedger
		}
	} else if state.Result.WalletVersion < 2 {
		return ErrInvalidLedger
	}
	return nil
}
