// Package operationlog records identifiers, never financial bodies or credentials.
package operationlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Record struct {
	Transport, CorrelationID, MessageID, TransactionID, WalletID, ProviderID string
	EventID, Status, FailureCode                                             string
	Replay                                                                   bool
}

func safe(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func Write(ctx context.Context, logger *slog.Logger, r Record) {
	if logger == nil {
		return
	}
	attrs := []slog.Attr{slog.Bool("idempotentReplay", r.Replay)}
	fields := []struct{ key, value string }{
		{"transport", r.Transport}, {"correlationId", r.CorrelationID},
		{"messageId", r.MessageID}, {"transactionId", r.TransactionID},
		{"walletId", r.WalletID}, {"providerId", r.ProviderID},
		{"eventId", r.EventID}, {"status", r.Status}, {"failureCode", r.FailureCode},
	}
	for _, field := range fields {
		if safe(field.value) {
			attrs = append(attrs, slog.String(field.key, field.value))
		}
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "financial operation completed", attrs...)
}

// Published decodes only identifier fields from an already committed snapshot.
// Called only after the publisher's durable acknowledgment succeeds.
func Published(ctx context.Context, logger *slog.Logger, eventID string, payload []byte) {
	if logger == nil {
		return
	}
	var envelope struct {
		CorrelationID string `json:"correlationId"`
		CausationID   string `json:"causationId"`
		Data          struct {
			TransactionID string `json:"transactionId"`
			WalletID      string `json:"walletId"`
			External      *struct {
				ProviderID string `json:"providerId"`
			} `json:"external"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return
	}
	r := Record{Transport: "outbox", EventID: eventID, Status: "PUBLISHED",
		CorrelationID: envelope.CorrelationID, TransactionID: envelope.Data.TransactionID,
		WalletID: envelope.Data.WalletID}
	if envelope.Data.External != nil {
		r.ProviderID = envelope.Data.External.ProviderID
	}
	// Causation is not always a message ID, so preserve its actual meaning.
	if safe(envelope.CausationID) {
		logger = logger.With("causationId", envelope.CausationID)
	}
	Write(ctx, logger, r)
}
