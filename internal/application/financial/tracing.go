package financial

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

// TracePort keeps the OpenTelemetry SDK out of financial use cases and domain.
type TracePort interface {
	Begin(context.Context, string) (context.Context, func(error))
}
type tracedUnit struct {
	Unit
	ctx    context.Context
	tracer TracePort
}

func (u tracedUnit) OpenWallet(_ context.Context, w wallet.Wallet, a, b string, m events.Metadata) error {
	ctx, end := u.tracer.Begin(u.ctx, "postgres.open_wallet")
	err := u.Unit.OpenWallet(ctx, w, a, b, m)
	end(err)
	return err
}
func (u tracedUnit) SaveDecision(_ context.Context, b tx.Transaction, d processing.Decision, m events.Metadata, s *Schedule) error {
	ctx, end := u.tracer.Begin(u.ctx, "postgres.persist_decision")
	err := u.Unit.SaveDecision(ctx, b, d, m, s)
	end(err)
	return err
}
func (u tracedUnit) LockWallet(_ context.Context, id string) (wallet.Wallet, error) {
	ctx, end := u.tracer.Begin(u.ctx, "postgres.lock_wallet")
	w, err := u.Unit.LockWallet(ctx, id)
	end(err)
	return w, err
}

type tracedBackend struct {
	Backend
	t TracePort
}

func (b tracedBackend) WithinFinancial(ctx context.Context, fn func(Unit) error) error {
	ctx, end := b.t.Begin(ctx, "financial.transaction")
	sql, finish := b.t.Begin(ctx, "postgres.transaction")
	err := b.Backend.WithinFinancial(sql, func(u Unit) error { return fn(tracedUnit{u, sql, b.t}) })
	finish(err)
	end(err)
	return err
}
func (b tracedBackend) ReadWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	ctx, end := b.t.Begin(ctx, "postgres.read_wallet")
	v, e := b.Backend.ReadWallet(ctx, id)
	end(e)
	return v, e
}
func (b tracedBackend) ReadTransaction(ctx context.Context, p, id string) (tx.Transaction, error) {
	ctx, end := b.t.Begin(ctx, "postgres.read_transaction")
	v, e := b.Backend.ReadTransaction(ctx, p, id)
	end(e)
	return v, e
}
func (b tracedBackend) ReadExternal(ctx context.Context, p, id string) (tx.Transaction, error) {
	ctx, end := b.t.Begin(ctx, "postgres.read_external")
	v, e := b.Backend.ReadExternal(ctx, p, id)
	end(e)
	return v, e
}
func (b tracedBackend) ReadLedger(ctx context.Context, id string, c Cursor, n int) (LedgerPage, error) {
	ctx, end := b.t.Begin(ctx, "postgres.read_ledger")
	v, e := b.Backend.ReadLedger(ctx, id, c, n)
	end(e)
	return v, e
}
func (b tracedBackend) Reconcile(ctx context.Context, id string) (Reconciliation, error) {
	ctx, end := b.t.Begin(ctx, "postgres.reconcile")
	v, e := b.Backend.Reconcile(ctx, id)
	end(e)
	return v, e
}
func TraceService(service *Service, t TracePort) *Service {
	return New(func() (Backend, error) {
		b, e := service.source()
		if e != nil {
			return nil, e
		}
		return tracedBackend{b, t}, nil
	})
}

type tracedInbox struct {
	InboxBackend
	t TracePort
}

func (b tracedInbox) WithinInbox(ctx context.Context, i InboxIdentity, fn func(Unit) error) error {
	ctx, end := b.t.Begin(ctx, "financial.inbox")
	sql, finish := b.t.Begin(ctx, "postgres.inbox_transaction")
	err := b.InboxBackend.WithinInbox(sql, i, func(u Unit) error { return fn(tracedUnit{u, sql, b.t}) })
	finish(err)
	end(err)
	return err
}
func TraceInbox(b InboxBackend, t TracePort) InboxBackend { return tracedInbox{b, t} }
