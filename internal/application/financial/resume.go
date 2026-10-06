package financial

import (
	"context"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

// ResumeDecision uses the same domain evaluator as HTTP. The caller must hold
// the durable work row, transaction row and wallet row locks in one SQL unit.
// An expired reference is rejected even if it arrives after the deadline.
func ResumeDecision(ctx context.Context, u Unit, before tx.Transaction, w wallet.Wallet, now time.Time, expired bool, correlation string) (processing.Decision, events.Metadata, error) {
	state, err := before.Snapshot()
	if err != nil {
		return processing.Decision{}, events.Metadata{}, err
	}
	metadata, entry, err := eventMetadata(Metadata{CorrelationID: correlation, CausationID: state.Input.ID})
	if err != nil {
		return processing.Decision{}, metadata, err
	}
	if expired {
		current, err := w.Snapshot()
		if err != nil {
			return processing.Decision{}, metadata, err
		}
		if now.Before(current.UpdatedAt) {
			now = current.UpdatedAt
		}
		if now.Before(state.UpdatedAt) {
			now = state.UpdatedAt
		}
		rejected, err := before.Reject(tx.ReferenceNotFound, &tx.Result{Balance: current.Balance, WalletVersion: current.Version}, "", now)
		return processing.Decision{Transaction: rejected, Wallet: w}, metadata, err
	}
	var reference *processing.Reference
	if state.Input.ReferenceExternalTransactionID != "" {
		reference, err = u.Reference(ctx, state.Input.ProviderID, state.Input.ReferenceExternalTransactionID, state.Input.WalletID)
		if err != nil {
			return processing.Decision{}, metadata, err
		}
	}
	decision, err := processing.Evaluate(processing.Request{Transaction: before, Wallet: w, Reference: reference, LedgerID: entry, Now: now})
	return decision, metadata, err
}
