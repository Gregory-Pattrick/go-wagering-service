package sqs

import (
	"context"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/delivery"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"strings"
	"testing"
)

const validRequest = `{"messageId":"envelope-1","type":"WagerTransactionRequested","occurredAt":"2026-10-06T10:00:00Z","data":{"providerId":"provider-a","externalTransactionId":"external-1","idempotencyKey":"original-key","playerId":"00000000-0000-4000-8000-000000000001","walletId":"00000000-0000-4000-8000-000000000002","roundId":"round","gameId":"game","kind":"BET","money":{"amount":"80.00","currency":"BRL"}}}`

func TestEnvelopeValidationAndCanonicalHash(t *testing.T) {
	original, err := DecodeRequest([]byte(validRequest))
	if err != nil {
		t.Fatal(err)
	}
	spaced := strings.ReplaceAll(validRequest, ",", ",\n  ")
	same, err := DecodeRequest([]byte(spaced))
	if err != nil || same.Hash != original.Hash {
		t.Fatal("whitespace changed hash", err)
	}
	changed, err := DecodeRequest([]byte(strings.Replace(validRequest, "80.00", "81.00", 1)))
	if err != nil || changed.Hash == original.Hash {
		t.Fatal("payload change not detected")
	}
	for _, body := range []string{
		strings.Replace(validRequest, `"messageId":"envelope-1"`, `"messageId":"envelope-1","messageId":"other"`, 1),
		strings.Replace(validRequest, `"amount":"80.00"`, `"amount":"80.00","amount":"1.00"`, 1),
		strings.Replace(validRequest, `"amount":"80.00"`, `"amount":80.00`, 1),
		strings.Replace(validRequest, `"idempotencyKey":"original-key",`, "", 1),
		strings.Replace(validRequest, `"gameId":"game"`, `"gameId":"game","unknown":1`, 1),
		strings.Replace(validRequest, `"gameId":"game"`, `"gameId":"game","referenceExternalTransactionId":null`, 1),
		validRequest + "{}", "null", strings.Repeat("x", 65537),
	} {
		if _, err := DecodeRequest([]byte(body)); err == nil {
			t.Fatalf("accepted invalid envelope: %.100s", body)
		}
	}
}
func TestUntrustedRoutingRejectedBeforeDatabase(t *testing.T) {
	called := false
	handler := NewRequestHandler(func() (financial.InboxBackend, error) { called = true; return nil, nil }, []string{"provider-b"})
	err := handler.Handle(context.Background(), delivery.Message{Body: []byte(validRequest), GroupID: "00000000-0000-4000-8000-000000000002"})
	if err == nil || called {
		t.Fatal("unknown provider reached database")
	}
}
