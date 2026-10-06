package sqs

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
)

type InboxSource func() (financial.InboxBackend, error)
type RequestHandler struct {
	source    InboxSource
	providers map[string]bool
}

func NewRequestHandler(source InboxSource, providers []string) *RequestHandler {
	allowed := map[string]bool{}
	for _, p := range providers {
		allowed[p] = true
	}
	return &RequestHandler{source: source, providers: allowed}
}
func (h *RequestHandler) Handle(ctx context.Context, message delivery.Message) error {
	requested, err := DecodeRequest(message.Body)
	if err != nil {
		return err
	}
	if message.GroupID != requested.Input.WalletID {
		return financial.Failure("INVALID_MESSAGE_GROUP")
	}
	if !h.providers[requested.Input.ProviderID] {
		return financial.Failure("UNKNOWN_PROVIDER")
	}
	// The shared queue is writable only by the trusted internal producer. The
	// provider field is routing data within that trust boundary, not an OIDC claim.
	principal, err := auth.NewProvider("trusted-sqs-ingress", requested.Input.ProviderID)
	if err != nil {
		return err
	}
	backend, err := h.source()
	if err != nil {
		return err
	}
	_, err = financial.SubmitInbox(ctx, backend, principal, requested.Input,
		financial.Metadata{CorrelationID: "sqs:" + requested.Hash, CausationID: requested.MessageID},
		financial.InboxIdentity{Consumer: "wager-transactions-v1", MessageID: requested.MessageID, Hash: requested.Hash, Provider: requested.Input.ProviderID, Key: requested.Input.IdempotencyKey})
	return err
}
