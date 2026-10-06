// Package transaction models financial transaction identity and lifecycle.
package transaction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
)

type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

type Status string

const (
	Pending          Status = "PENDING"
	PendingReference Status = "PENDING_REFERENCE"
	Processed        Status = "PROCESSED"
	Rejected         Status = "REJECTED"
	Failed           Status = "FAILED"
)

var (
	ErrInvalidIdentity    = errors.New("invalid transaction identity")
	ErrInvalidKind        = errors.New("invalid transaction kind for this origin")
	ErrInvalidAmount      = errors.New("invalid amount for transaction kind")
	ErrInvalidReference   = errors.New("invalid transaction reference")
	ErrInvalidState       = errors.New("invalid transaction state")
	ErrInvalidTransition  = errors.New("invalid transaction transition")
	ErrInvalidResult      = errors.New("invalid transaction result")
	ErrInvalidTime        = errors.New("invalid transaction timestamps")
	ErrInvalidHash        = errors.New("transaction payload hash mismatch")
	ErrInvalidFailureCode = errors.New("invalid terminal failure code")
)

type FailureCode string

const (
	InsufficientFunds              FailureCode = "INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds      FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	ReferenceNotFound              FailureCode = "REFERENCE_NOT_FOUND"
	ReferenceNotProcessed          FailureCode = "REFERENCE_NOT_PROCESSED"
	ReferenceMismatch              FailureCode = "REFERENCE_MISMATCH"
	ReferenceKindInvalid           FailureCode = "REFERENCE_KIND_INVALID"
	AlreadyReversed                FailureCode = "ALREADY_REVERSED"
	WalletNotFound                 FailureCode = "WALLET_NOT_FOUND"
	WalletMismatch                 FailureCode = "WALLET_MISMATCH"
	BalanceOverflow                FailureCode = "BALANCE_OVERFLOW"
	PermanentInfrastructureFailure FailureCode = "PERMANENT_INFRASTRUCTURE_FAILURE"
)

// ExternalInput contains domain identities only; transport metadata is excluded.
type ExternalInput struct {
	ID                             string
	ExternalTransactionID          string
	ProviderID                     string
	IdempotencyKey                 string
	WalletID                       string
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
}

type OpeningInput struct {
	ID       string
	WalletID string
	PlayerID string
	Money    money.Money
}

// Result is the financial snapshot returned for an original decision and replay.
// It must never be reconstructed from a wallet's current balance on replay.
type Result struct {
	Balance       money.Money
	WalletVersion int64
}

// State is detached from the aggregate. Result is copied on input and output.
type State struct {
	Input               ExternalInput
	PayloadHash         string
	Status              Status
	ResolvedReferenceID string
	FailureCode         FailureCode
	Result              *Result
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Transaction struct{ state State }

func NewExternal(input ExternalInput, now time.Time) (Transaction, error) {
	if input.Kind == Opening {
		return Transaction{}, ErrInvalidKind
	}
	if err := validateInput(input); err != nil {
		return Transaction{}, err
	}
	hash, err := payloadHash(input)
	if err != nil {
		return Transaction{}, err
	}
	return Rehydrate(State{Input: input, PayloadHash: hash, Status: Pending, CreatedAt: now, UpdatedAt: now})
}

// NewOpening uses a caller-supplied stable internal ID and no provider metadata.
// Zero-balance wallet creation must not construct an opening transaction.
func NewOpening(input OpeningInput, now time.Time) (Transaction, error) {
	return Rehydrate(State{Input: ExternalInput{ID: input.ID, WalletID: input.WalletID,
		PlayerID: input.PlayerID, Kind: Opening, Money: input.Money}, Status: Pending,
		CreatedAt: now, UpdatedAt: now})
}

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
func validText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func validateInput(input ExternalInput) error {
	if !validUUID(input.ID) || !validUUID(input.WalletID) || !validUUID(input.PlayerID) {
		return ErrInvalidIdentity
	}
	sign, err := input.Money.Sign()
	if err != nil {
		return err
	}
	switch input.Kind {
	case Opening, Bet, Win, Refund, Rollback:
		if sign <= 0 {
			return ErrInvalidAmount
		}
	case Loss:
		if sign != 0 {
			return ErrInvalidAmount
		}
	default:
		return ErrInvalidKind
	}
	if input.Kind == Opening {
		if input.ProviderID != "" || input.ExternalTransactionID != "" || input.IdempotencyKey != "" || input.RoundID != "" || input.GameID != "" || input.ReferenceExternalTransactionID != "" {
			return ErrInvalidIdentity
		}
		return nil
	}
	for _, value := range []string{input.ProviderID, input.ExternalTransactionID, input.IdempotencyKey, input.RoundID, input.GameID} {
		if !validText(value) {
			return ErrInvalidIdentity
		}
	}
	ref := input.ReferenceExternalTransactionID
	if ref != "" && (!validText(ref) || ref == input.ExternalTransactionID) {
		return ErrInvalidReference
	}
	switch input.Kind {
	case Bet, Loss:
		if ref != "" {
			return ErrInvalidReference
		}
	case Refund, Rollback:
		if ref == "" {
			return ErrInvalidReference
		}
	}
	return nil
}

// Fields are encoded in lexical key order. Money encodes amount before currency.
// Internal ID, idempotency key and transport metadata do not affect this hash.
func payloadHash(input ExternalInput) (string, error) {
	data, err := json.Marshal(struct {
		ExternalTransactionID          string      `json:"externalTransactionId"`
		GameID                         string      `json:"gameId"`
		Kind                           Kind        `json:"kind"`
		Money                          money.Money `json:"money"`
		PlayerID                       string      `json:"playerId"`
		ProviderID                     string      `json:"providerId"`
		ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId"`
		RoundID                        string      `json:"roundId"`
		WalletID                       string      `json:"walletId"`
	}{input.ExternalTransactionID, input.GameID, input.Kind, input.Money, input.PlayerID, input.ProviderID, input.ReferenceExternalTransactionID, input.RoundID, input.WalletID})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func copyState(state State) State {
	if state.Result != nil {
		result := *state.Result
		state.Result = &result
	}
	return state
}
func validRejection(code FailureCode) bool {
	switch code {
	case InsufficientFunds, ReversalInsufficientFunds, ReferenceNotFound, ReferenceNotProcessed,
		ReferenceMismatch, ReferenceKindInvalid, AlreadyReversed, WalletNotFound, WalletMismatch, BalanceOverflow:
		return true
	default:
		return false
	}
}
func validateResult(result *Result, input ExternalInput) error {
	if result == nil {
		return ErrInvalidResult
	}
	sign, err := result.Balance.Sign()
	if err != nil || sign < 0 || result.WalletVersion < 1 {
		return ErrInvalidResult
	}
	if _, err := input.Money.Compare(result.Balance); err != nil {
		return ErrInvalidResult
	}
	return nil
}

func Rehydrate(state State) (Transaction, error) {
	if err := validateInput(state.Input); err != nil {
		return Transaction{}, err
	}
	if state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() || state.UpdatedAt.Before(state.CreatedAt) {
		return Transaction{}, ErrInvalidTime
	}
	if state.Input.Kind == Opening {
		if state.PayloadHash != "" || state.ResolvedReferenceID != "" {
			return Transaction{}, ErrInvalidHash
		}
	} else {
		expected, err := payloadHash(state.Input)
		if err != nil {
			return Transaction{}, err
		}
		if state.PayloadHash != expected {
			return Transaction{}, ErrInvalidHash
		}
	}
	if state.ResolvedReferenceID != "" && (!validUUID(state.ResolvedReferenceID) || state.ResolvedReferenceID == state.Input.ID || state.Input.ReferenceExternalTransactionID == "") {
		return Transaction{}, ErrInvalidReference
	}
	switch state.Status {
	case Pending, PendingReference:
		if state.FailureCode != "" || state.Result != nil || state.ResolvedReferenceID != "" {
			return Transaction{}, ErrInvalidState
		}
		if state.Status == PendingReference && state.Input.ReferenceExternalTransactionID == "" {
			return Transaction{}, ErrInvalidReference
		}
	case Processed:
		if state.FailureCode != "" {
			return Transaction{}, ErrInvalidFailureCode
		}
		if err := validateResult(state.Result, state.Input); err != nil {
			return Transaction{}, err
		}
		if state.Input.ReferenceExternalTransactionID != "" && state.ResolvedReferenceID == "" {
			return Transaction{}, ErrInvalidReference
		}
		if state.Input.Kind == Opening {
			comparison, _ := state.Input.Money.Compare(state.Result.Balance)
			if comparison != 0 || state.Result.WalletVersion != 1 {
				return Transaction{}, ErrInvalidResult
			}
		}
	case Rejected:
		if !validRejection(state.FailureCode) {
			return Transaction{}, ErrInvalidFailureCode
		}
		if state.Result != nil {
			if err := validateResult(state.Result, state.Input); err != nil {
				return Transaction{}, err
			}
		}
	case Failed:
		if state.FailureCode != PermanentInfrastructureFailure {
			return Transaction{}, ErrInvalidFailureCode
		}
		if state.Result != nil {
			return Transaction{}, ErrInvalidResult
		}
	default:
		return Transaction{}, ErrInvalidState
	}
	state.CreatedAt = state.CreatedAt.UTC()
	state.UpdatedAt = state.UpdatedAt.UTC()
	return Transaction{state: copyState(state)}, nil
}
func (t Transaction) Validate() error { _, err := Rehydrate(t.state); return err }
func (t Transaction) Snapshot() (State, error) {
	if err := t.Validate(); err != nil {
		return State{}, err
	}
	return copyState(t.state), nil
}

func (t Transaction) transition(status Status, code FailureCode, result *Result, referenceID string, now time.Time) (Transaction, error) {
	if err := t.Validate(); err != nil {
		return Transaction{}, err
	}
	if t.state.Status != Pending && t.state.Status != PendingReference {
		return Transaction{}, ErrInvalidTransition
	}
	if status == PendingReference && t.state.Status != Pending {
		return Transaction{}, ErrInvalidTransition
	}
	if now.IsZero() {
		return Transaction{}, ErrInvalidTime
	}
	next := copyState(t.state)
	next.Status = status
	next.FailureCode = code
	next.Result = result
	next.ResolvedReferenceID = referenceID
	next.UpdatedAt = now.UTC()
	if next.UpdatedAt.Before(t.state.UpdatedAt) {
		next.UpdatedAt = t.state.UpdatedAt
	}
	return Rehydrate(next)
}
func (t Transaction) WaitForReference(now time.Time) (Transaction, error) {
	return t.transition(PendingReference, "", nil, "", now)
}

// MarkProcessed records a decision made by the financial application service.
// That service must validate the reference and atomically persist wallet, ledger,
// transaction and outbox; this lifecycle method does not apply financial effects.
func (t Transaction) MarkProcessed(result Result, referenceID string, now time.Time) (Transaction, error) {
	return t.transition(Processed, "", &result, referenceID, now)
}
func (t Transaction) Reject(code FailureCode, result *Result, referenceID string, now time.Time) (Transaction, error) {
	return t.transition(Rejected, code, result, referenceID, now)
}

// FailPermanent must not be used for retryable outages or unknown commit outcomes.
func (t Transaction) FailPermanent(now time.Time) (Transaction, error) {
	return t.transition(Failed, PermanentInfrastructureFailure, nil, "", now)
}
