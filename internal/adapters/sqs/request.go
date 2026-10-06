package sqs

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/application/financial"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
)

type Requested struct {
	MessageID  string
	OccurredAt time.Time
	Input      tx.ExternalInput
	Hash       string
}
type requestData struct {
	ProviderID string          `json:"providerId"`
	ExternalID string          `json:"externalTransactionId"`
	Key        string          `json:"idempotencyKey"`
	PlayerID   string          `json:"playerId"`
	WalletID   string          `json:"walletId"`
	RoundID    string          `json:"roundId"`
	GameID     string          `json:"gameId"`
	Kind       tx.Kind         `json:"kind"`
	Money      json.RawMessage `json:"money"`
	Reference  string          `json:"referenceExternalTransactionId,omitempty"`
}

// Reject duplicate keys at every depth, including money, before decoding a DTO.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return fmt.Errorf("JSON nesting too deep")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}
func DecodeRequest(body []byte) (Requested, error) {
	invalid := func() (Requested, error) { return Requested{}, financial.Failure("INVALID_ENVELOPE") }
	if len(body) == 0 || len(body) > 65536 || !utf8.Valid(body) {
		return invalid()
	}
	duplicateCheck := json.NewDecoder(bytes.NewReader(body))
	duplicateCheck.UseNumber()
	if err := uniqueJSON(duplicateCheck, 0); err != nil {
		return invalid()
	}
	if _, err := duplicateCheck.Token(); err != io.EOF {
		return invalid()
	}
	var wire struct {
		MessageID  string      `json:"messageId"`
		Type       string      `json:"type"`
		OccurredAt string      `json:"occurredAt"`
		Data       requestData `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return invalid()
	}
	if wire.Type != "WagerTransactionRequested" || !financial.Text(wire.MessageID, 256) {
		return invalid()
	}
	at, err := time.Parse(time.RFC3339Nano, wire.OccurredAt)
	if err != nil || at.IsZero() {
		return invalid()
	}
	_, offset := at.Zone()
	if offset != 0 {
		return invalid()
	}
	var rawEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawEnvelope); err != nil {
		return invalid()
	}
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(rawEnvelope["data"], &rawData); err != nil {
		return invalid()
	}
	topKeys := map[string]bool{"messageId": true, "type": true, "occurredAt": true, "data": true}
	dataKeys := map[string]bool{"providerId": true, "externalTransactionId": true, "idempotencyKey": true, "playerId": true, "walletId": true, "roundId": true, "gameId": true, "kind": true, "money": true, "referenceExternalTransactionId": true}
	for key := range rawEnvelope {
		if !topKeys[key] {
			return invalid()
		}
	}
	for key := range rawData {
		if !dataKeys[key] {
			return invalid()
		}
	}
	d := wire.Data
	if _, present := rawData["referenceExternalTransactionId"]; present && d.Reference == "" {
		return invalid()
	}
	for _, value := range []string{d.ProviderID, d.ExternalID, d.RoundID, d.GameID} {
		if !financial.Text(value, 256) {
			return invalid()
		}
	}
	if !financial.Text(d.Key, 512) || (d.Reference != "" && !financial.Text(d.Reference, 256)) {
		return invalid()
	}
	d.WalletID, err = financial.CanonicalID(d.WalletID)
	if err != nil {
		return invalid()
	}
	d.PlayerID, err = financial.CanonicalID(d.PlayerID)
	if err != nil {
		return invalid()
	}
	amount, err := money.ParseJSON(d.Money)
	if err != nil {
		return invalid()
	}
	canonicalMoney, err := json.Marshal(amount)
	if err != nil {
		return invalid()
	}
	d.Money = canonicalMoney
	wire.Data = d
	wire.OccurredAt = at.UTC().Format(time.RFC3339Nano)
	canonical, err := json.Marshal(wire)
	if err != nil {
		return invalid()
	}
	return Requested{MessageID: wire.MessageID, OccurredAt: at.UTC(), Hash: fmt.Sprintf("%x", sha256.Sum256(canonical)), Input: tx.ExternalInput{
		ProviderID: d.ProviderID, ExternalTransactionID: d.ExternalID, IdempotencyKey: d.Key,
		PlayerID: d.PlayerID, WalletID: d.WalletID, RoundID: d.RoundID, GameID: d.GameID,
		Kind: d.Kind, Money: amount, ReferenceExternalTransactionID: d.Reference,
	}}, nil
}
