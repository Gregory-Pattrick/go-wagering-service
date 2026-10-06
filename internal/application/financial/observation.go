package financial

import (
	"context"
	"errors"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

// Observer is an application port. No telemetry library enters the domain.
type Observer interface {
	FinancialCompleted(string, string, bool)
	FinancialError(string, string)
}
type observedBackend struct {
	Backend
	observer  Observer
	transport string
}
type observedUnit struct {
	Unit
	status string
	replay bool
}

func (u *observedUnit) InsertTransaction(ctx context.Context, t tx.Transaction) error {
	err := u.Unit.InsertTransaction(ctx, t)
	if errors.Is(err, ErrIdentityTaken) {
		u.replay = true
	}
	return err
}
func (u *observedUnit) ByKey(ctx context.Context, p, k string) (tx.Transaction, error) {
	t, err := u.Unit.ByKey(ctx, p, k)
	if err == nil {
		s, _ := t.Snapshot()
		u.status = string(s.Status)
	}
	return t, err
}
func (u *observedUnit) SaveDecision(ctx context.Context, before tx.Transaction, d processing.Decision, m events.Metadata, s *Schedule) error {
	err := u.Unit.SaveDecision(ctx, before, d, m, s)
	if err == nil {
		state, _ := d.Transaction.Snapshot()
		u.status = string(state.Status)
	}
	return err
}
func (u *observedUnit) OpenWallet(ctx context.Context, w wallet.Wallet, a, b string, m events.Metadata) error {
	err := u.Unit.OpenWallet(ctx, w, a, b, m)
	if err == nil {
		u.status = "OPENED"
	}
	return err
}
func ObserveSQL(observer Observer, transport string, run func(func(Unit) error) error, fn func(Unit) error) error {
	var observed *observedUnit
	err := run(func(u Unit) error { observed = &observedUnit{Unit: u, status: "UNKNOWN"}; return fn(observed) })
	if err == nil && observed != nil {
		observer.FinancialCompleted(transport, observed.status, observed.replay)
	}
	if err != nil {
		category := "unavailable"
		var problem *Error
		if errors.Is(err, ErrRetryable) {
			category = "database_conflict"
		} else if errors.Is(err, ErrUnique) {
			category = "identity_conflict"
		} else if errors.Is(err, ErrOutcomeUnknown) {
			category = "commit_unknown"
		} else if errors.As(err, &problem) {
			switch problem.Code {
			case "IDEMPOTENCY_CONFLICT", "EXTERNAL_TRANSACTION_CONFLICT", "INBOX_PAYLOAD_CONFLICT":
				category = "identity_conflict"
			default:
				category = "validation"
			}
		}
		observer.FinancialError(transport, category)
	}
	return err
}
func (b observedBackend) WithinFinancial(ctx context.Context, fn func(Unit) error) error {
	return ObserveSQL(b.observer, b.transport, func(callback func(Unit) error) error { return b.Backend.WithinFinancial(ctx, callback) }, fn)
}
func ObserveService(service *Service, observer Observer) *Service {
	return New(func() (Backend, error) {
		backend, err := service.source()
		if err != nil {
			return nil, err
		}
		return observedBackend{backend, observer, "http"}, nil
	})
}

type observedInbox struct {
	InboxBackend
	observer Observer
}

func (b observedInbox) WithinInbox(ctx context.Context, i InboxIdentity, fn func(Unit) error) error {
	return ObserveSQL(b.observer, "sqs", func(callback func(Unit) error) error { return b.InboxBackend.WithinInbox(ctx, i, callback) }, fn)
}
func ObserveInbox(backend InboxBackend, observer Observer) InboxBackend {
	return observedInbox{backend, observer}
}
