package operationlog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestPublishedLogsIdentifiersWithoutFinancialPayload(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	Published(context.Background(), logger, "event-1", []byte(`{"correlationId":"correlation-1","causationId":"message-1","data":{"transactionId":"transaction-1","walletId":"wallet-1","external":{"providerId":"provider-a","idempotencyKey":"private-key"},"money":{"amount":"900.00","currency":"BRL"},"unexpectedSecret":"secret-value"}}`))
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"eventId": "event-1", "transactionId": "transaction-1", "walletId": "wallet-1", "providerId": "provider-a", "correlationId": "correlation-1", "causationId": "message-1", "status": "PUBLISHED"} {
		if record[key] != value {
			t.Fatalf("missing %s", key)
		}
	}
	for _, forbidden := range []string{"900.00", "private-key", "secret-value", "money", "payload"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
}

func TestReplayAndUnsafeMetadata(t *testing.T) {
	var output bytes.Buffer
	Write(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)), Record{
		Transport: "sqs", CorrelationID: "correlation-1", MessageID: "message-1", TransactionID: "transaction-1",
		WalletID: "wallet-1", ProviderID: "provider-a", Status: "REJECTED", FailureCode: "INSUFFICIENT_FUNDS", Replay: true,
		EventID: "invalid\nmetadata",
	})
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["idempotentReplay"] != true || record["messageId"] != "message-1" || record["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatal(record)
	}
	if _, exists := record["eventId"]; exists {
		t.Fatal("unsafe identifier included")
	}
}
