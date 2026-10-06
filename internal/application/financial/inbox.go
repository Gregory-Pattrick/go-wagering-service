package financial

import (
	"context"
	"fmt"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
)

// InboxIdentity belongs to the envelope, not to the SQS delivery ID or receipt.
// Provider and Key identify the result committed by the shared Submit use case.
type InboxIdentity struct{ Consumer, MessageID, Hash, Provider, Key string }
type InboxBackend interface {
	Backend
	WithinInbox(context.Context, InboxIdentity, func(Unit) error) error
}
type inboxBound struct {
	Backend
	store    InboxBackend
	identity InboxIdentity
}

func (b inboxBound) WithinFinancial(ctx context.Context, fn func(Unit) error) error {
	return b.store.WithinInbox(ctx, b.identity, fn)
}

// SubmitInbox shares Submit itself with HTTP: authorization, validation,
// persistent replay and domain processing are not implemented a second time.
// The backend decorator adds inbox locking/completion around that same SQL unit.
func SubmitInbox(ctx context.Context, backend InboxBackend, p auth.Principal, input tx.ExternalInput, m Metadata, identity InboxIdentity) (TransactionResponse, error) {
	if !Text(identity.Consumer, 128) || !Text(identity.MessageID, 256) || len(identity.Hash) != 64 {
		return TransactionResponse{}, Failure("INVALID_ENVELOPE")
	}
	for _, c := range identity.Hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return TransactionResponse{}, Failure("INVALID_ENVELOPE")
		}
	}
	if identity.Provider != input.ProviderID || identity.Key != input.IdempotencyKey {
		return TransactionResponse{}, fmt.Errorf("inbox identity does not match input")
	}
	bound := inboxBound{Backend: backend, store: backend, identity: identity}
	service := New(func() (Backend, error) { return bound, nil })
	return service.Submit(ctx, p, input, m)
}
