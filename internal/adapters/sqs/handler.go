package sqs

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/auth"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/operationlog"
	"log/slog"
)

type InboxSource func() (financial.InboxBackend, error)
type RequestHandler struct {
	source    InboxSource
	providers map[string]bool
	logger    *slog.Logger
}

func NewRequestHandler(source InboxSource, providers []string, loggers ...*slog.Logger) *RequestHandler {
	allowed := map[string]bool{}
	for _, p := range providers {
		allowed[p] = true
	}
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &RequestHandler{source: source, providers: allowed, logger: logger}
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
	result, err := financial.SubmitInbox(ctx, backend, principal, requested.Input,
		financial.Metadata{CorrelationID: "sqs:" + requested.Hash, CausationID: requested.MessageID},
		financial.InboxIdentity{Consumer: "wager-transactions-v1", MessageID: requested.MessageID, Hash: requested.Hash, Provider: requested.Input.ProviderID, Key: requested.Input.IdempotencyKey})
	if err == nil {
		operationlog.Write(ctx, h.logger, operationlog.Record{Transport: "sqs", CorrelationID: "sqs:" + requested.Hash, MessageID: requested.MessageID, TransactionID: result.TransactionID, WalletID: requested.Input.WalletID, ProviderID: requested.Input.ProviderID, Status: string(result.Status), FailureCode: string(result.FailureCode), Replay: result.IdempotentReplay})
	}
	return err
}
