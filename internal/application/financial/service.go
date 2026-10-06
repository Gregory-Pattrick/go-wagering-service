// Package financial contains use cases shared by authenticated transports.
package financial

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrIdentityTaken  = errors.New("identity already exists")
	ErrUnique         = errors.New("unique constraint conflict")
	ErrRetryable      = errors.New("retryable database conflict")
	ErrOutcomeUnknown = errors.New("commit outcome unknown")
)

type Error struct{ Code string }

func (e *Error) Error() string  { return e.Code }
func Failure(code string) error { return &Error{Code: code} }

type Metadata struct{ CorrelationID, CausationID string }
type Schedule struct{ NextAttemptAt, ExpiresAt time.Time }
type Unit interface {
	OpenWallet(context.Context, wallet.Wallet, string, string, events.Metadata) error
	InsertTransaction(context.Context, tx.Transaction) error
	ByKey(context.Context, string, string) (tx.Transaction, error)
	ByExternal(context.Context, string, string) (tx.Transaction, error)
	LockWallet(context.Context, string) (wallet.Wallet, error)
	Reference(context.Context, string, string, string) (*processing.Reference, error)
	SaveDecision(context.Context, tx.Transaction, processing.Decision, events.Metadata, *Schedule) error
}
type Backend interface {
	WithinFinancial(context.Context, func(Unit) error) error
	ReadWallet(context.Context, string) (wallet.Wallet, error)
	ReadTransaction(context.Context, string, string) (tx.Transaction, error)
	ReadExternal(context.Context, string, string) (tx.Transaction, error)
	ReadLedger(context.Context, string, Cursor, int) (LedgerPage, error)
	Reconcile(context.Context, string) (Reconciliation, error)
}
type Source func() (Backend, error)
type Service struct {
	source     Source
	mismatches atomic.Uint64
}

func New(source Source) *Service                    { return &Service{source: source} }
func (s *Service) ReconciliationMismatches() uint64 { return s.mismatches.Load() }
func Clock() time.Time                              { return time.Now().UTC().Truncate(time.Microsecond) }
func (s *Service) backend() (Backend, error) {
	b, err := s.source()
	if err != nil {
		return nil, err
	}
	return b, nil
}
func within(ctx context.Context, b Backend, fn func(Unit) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := b.WithinFinancial(ctx, fn)
		if !errors.Is(err, ErrRetryable) || attempt == 2 {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ErrRetryable
}
func eventMetadata(m Metadata) (events.Metadata, string, error) {
	ids := make([]string, 3)
	for i := range ids {
		var err error
		ids[i], err = NewID()
		if err != nil {
			return events.Metadata{}, "", err
		}
	}
	return events.Metadata{TransactionEventID: ids[0], BalanceEventID: ids[1], CorrelationID: m.CorrelationID, CausationID: m.CausationID}, ids[2], nil
}
func (s *Service) Open(ctx context.Context, p auth.Principal, player string, balance money.Money, m Metadata) (WalletResponse, error) {
	if !p.IsInternal() {
		return WalletResponse{}, auth.ErrForbidden
	}
	if !Text(m.CorrelationID, 128) || (m.CausationID != "" && !Text(m.CausationID, 256)) {
		return WalletResponse{}, Failure("INVALID_METADATA")
	}
	player, err := CanonicalID(player)
	if err != nil {
		return WalletResponse{}, err
	}
	sign, err := balance.Sign()
	if err != nil || sign < 0 {
		return WalletResponse{}, Failure("INVALID_MONEY")
	}
	walletID, err := NewID()
	if err != nil {
		return WalletResponse{}, err
	}
	openingID, err := NewID()
	if err != nil {
		return WalletResponse{}, err
	}
	metadata, entryID, err := eventMetadata(m)
	if err != nil {
		return WalletResponse{}, err
	}
	w, err := wallet.New(walletID, player, balance, Clock())
	if err != nil {
		return WalletResponse{}, err
	}
	b, err := s.backend()
	if err != nil {
		return WalletResponse{}, err
	}
	err = within(ctx, b, func(u Unit) error { return u.OpenWallet(ctx, w, openingID, entryID, metadata) })
	if errors.Is(err, ErrUnique) {
		return WalletResponse{}, Failure("WALLET_ALREADY_EXISTS")
	}
	if err != nil {
		return WalletResponse{}, err
	}
	return WalletView(w)
}

// Submit accepts the exact caller-supplied idempotency key. It never replaces it
// with a derived key. Provider authorization happens before identity lookup.
func (s *Service) Submit(ctx context.Context, p auth.Principal, input tx.ExternalInput, m Metadata) (TransactionResponse, error) {
	if err := p.AuthorizeProvider(input.ProviderID); err != nil {
		return TransactionResponse{}, err
	}
	if !Text(m.CorrelationID, 128) || (m.CausationID != "" && !Text(m.CausationID, 256)) {
		return TransactionResponse{}, Failure("INVALID_METADATA")
	}
	for _, value := range []string{input.ProviderID, input.ExternalTransactionID, input.RoundID, input.GameID} {
		if !Text(value, 256) {
			return TransactionResponse{}, Failure("INVALID_IDENTIFIER")
		}
	}
	if !Text(input.IdempotencyKey, 512) {
		return TransactionResponse{}, Failure("INVALID_IDEMPOTENCY_KEY")
	}
	if input.ReferenceExternalTransactionID != "" && !Text(input.ReferenceExternalTransactionID, 256) {
		return TransactionResponse{}, Failure("INVALID_IDENTIFIER")
	}
	var err error
	input.WalletID, err = CanonicalID(input.WalletID)
	if err != nil {
		return TransactionResponse{}, err
	}
	input.PlayerID, err = CanonicalID(input.PlayerID)
	if err != nil {
		return TransactionResponse{}, err
	}
	input.ID, err = NewID()
	if err != nil {
		return TransactionResponse{}, err
	}
	pending, err := tx.NewExternal(input, Clock())
	if err != nil {
		return TransactionResponse{}, Failure("INVALID_OPERATION")
	}
	requested, _ := pending.Snapshot()
	metadata, entryID, err := eventMetadata(m)
	if err != nil {
		return TransactionResponse{}, err
	}
	b, err := s.backend()
	if err != nil {
		return TransactionResponse{}, err
	}
	var response TransactionResponse
	err = within(ctx, b, func(u Unit) error {
		response = TransactionResponse{}
		err := u.InsertTransaction(ctx, pending)
		if errors.Is(err, ErrIdentityTaken) {
			existing, lookupErr := u.ByKey(ctx, input.ProviderID, input.IdempotencyKey)
			if lookupErr == nil {
				stored, _ := existing.Snapshot()
				if stored.PayloadHash != requested.PayloadHash {
					return Failure("IDEMPOTENCY_CONFLICT")
				}
				response, lookupErr = TransactionView(existing, true)
				return lookupErr
			}
			if !errors.Is(lookupErr, ErrNotFound) {
				return lookupErr
			}
			if _, lookupErr = u.ByExternal(ctx, input.ProviderID, input.ExternalTransactionID); lookupErr == nil {
				return Failure("EXTERNAL_TRANSACTION_CONFLICT")
			}
			if !errors.Is(lookupErr, ErrNotFound) {
				return lookupErr
			}
			return fmt.Errorf("unresolved identity collision")
		}
		if err != nil {
			return err
		}
		w, err := u.LockWallet(ctx, input.WalletID)
		if errors.Is(err, ErrNotFound) {
			rejected, e := pending.Reject(tx.WalletNotFound, nil, "", Clock())
			if e != nil {
				return e
			}
			if e = u.SaveDecision(ctx, pending, processing.Decision{Transaction: rejected}, metadata, nil); e != nil {
				return e
			}
			response, e = TransactionView(rejected, false)
			return e
		}
		if err != nil {
			return err
		}
		var reference *processing.Reference
		if input.ReferenceExternalTransactionID != "" {
			reference, err = u.Reference(ctx, input.ProviderID, input.ReferenceExternalTransactionID, input.WalletID)
			if err != nil {
				return err
			}
		}
		decision, err := processing.Evaluate(processing.Request{Transaction: pending, Wallet: w, Reference: reference, LedgerID: entryID, Now: Clock()})
		if err != nil {
			return err
		}
		schedule := &Schedule{NextAttemptAt: Clock().Add(time.Second), ExpiresAt: requested.CreatedAt.Add(15 * time.Minute)}
		if err = u.SaveDecision(ctx, pending, decision, metadata, schedule); err != nil {
			return err
		}
		response, err = TransactionView(decision.Transaction, false)
		return err
	})
	if err != nil {
		return TransactionResponse{}, err
	}
	return response, nil
}
func (s *Service) Wallet(ctx context.Context, p auth.Principal, id string) (WalletResponse, error) {
	if !p.IsInternal() {
		return WalletResponse{}, auth.ErrForbidden
	}
	id, err := CanonicalID(id)
	if err != nil {
		return WalletResponse{}, err
	}
	b, err := s.backend()
	if err != nil {
		return WalletResponse{}, err
	}
	w, err := b.ReadWallet(ctx, id)
	if err != nil {
		return WalletResponse{}, err
	}
	return WalletView(w)
}
func (s *Service) Transaction(ctx context.Context, p auth.Principal, id string) (TransactionResponse, error) {
	if !p.IsProvider() {
		return TransactionResponse{}, auth.ErrForbidden
	}
	id, err := CanonicalID(id)
	if err != nil {
		return TransactionResponse{}, err
	}
	b, err := s.backend()
	if err != nil {
		return TransactionResponse{}, err
	}
	tr, err := b.ReadTransaction(ctx, p.ProviderID(), id)
	if err != nil {
		return TransactionResponse{}, err
	}
	return TransactionView(tr, false)
}
func (s *Service) External(ctx context.Context, p auth.Principal, provider, external string) (TransactionResponse, error) {
	if err := p.AuthorizeProvider(provider); err != nil {
		return TransactionResponse{}, err
	}
	if !Text(external, 256) {
		return TransactionResponse{}, Failure("INVALID_IDENTIFIER")
	}
	b, err := s.backend()
	if err != nil {
		return TransactionResponse{}, err
	}
	tr, err := b.ReadExternal(ctx, provider, external)
	if err != nil {
		return TransactionResponse{}, err
	}
	return TransactionView(tr, false)
}
func (s *Service) Ledger(ctx context.Context, p auth.Principal, id, cursor string, limit int) (LedgerPage, error) {
	if !p.IsInternal() {
		return LedgerPage{}, auth.ErrForbidden
	}
	id, err := CanonicalID(id)
	if err != nil {
		return LedgerPage{}, err
	}
	if limit < 1 || limit > 100 {
		return LedgerPage{}, Failure("INVALID_PAGINATION")
	}
	c, err := DecodeCursor(cursor, id)
	if err != nil {
		return LedgerPage{}, err
	}
	b, err := s.backend()
	if err != nil {
		return LedgerPage{}, err
	}
	return b.ReadLedger(ctx, id, c, limit)
}
func (s *Service) Reconcile(ctx context.Context, p auth.Principal, id string) (Reconciliation, error) {
	if !p.IsInternal() {
		return Reconciliation{}, auth.ErrForbidden
	}
	id, err := CanonicalID(id)
	if err != nil {
		return Reconciliation{}, err
	}
	b, err := s.backend()
	if err != nil {
		return Reconciliation{}, err
	}
	r, err := b.Reconcile(ctx, id)
	if err == nil && !r.Consistent {
		s.mismatches.Add(1)
	}
	return r, err
}
