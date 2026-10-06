package events_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/events"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
)

const transactionID = "0192f291-27dd-7d3f-8071-5f8685deef31"
const walletID = "0192f291-27dd-7d3f-8071-5f8685deef32"
const playerID = "0192f291-27dd-7d3f-8071-5f8685deef33"
const ledgerID = "0192f291-27dd-7d3f-8071-5f8685deef34"
const referenceID = "0192f291-27dd-7d3f-8071-5f8685deef35"
const transactionEventID = "0192f291-27dd-7d3f-8071-5f8685deef36"
const balanceEventID = "0192f291-27dd-7d3f-8071-5f8685deef37"

var now = time.Date(2026, 10, 5, 22, 0, 0, 0, time.FixedZone("local", -3*3600))

func cash(t *testing.T, n int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(n, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func metadata() events.Metadata {
	return events.Metadata{TransactionEventID: transactionEventID, BalanceEventID: balanceEventID, CorrelationID: "request-123", CausationID: "message-123"}
}
func pending(t *testing.T, kind tx.Kind) tx.Transaction {
	t.Helper()
	if kind == tx.Opening {
		tr, err := tx.NewOpening(tx.OpeningInput{ID: transactionID, WalletID: walletID, PlayerID: playerID, Money: cash(t, 100)}, now)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	n := int64(100)
	if kind == tx.Loss {
		n = 0
	}
	i := tx.ExternalInput{ID: transactionID, ExternalTransactionID: "operation", ProviderID: "provider-a", IdempotencyKey: "key", WalletID: walletID, PlayerID: playerID, RoundID: "round", GameID: "game", Kind: kind, Money: cash(t, n)}
	if kind == tx.Refund || kind == tx.Rollback {
		i.ReferenceExternalTransactionID = "original"
	}
	tr, err := tx.NewExternal(i, now)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func processed(t *testing.T, kind tx.Kind) (tx.Transaction, tx.Transaction, *ledger.Entry) {
	t.Helper()
	before := pending(t, kind)
	balanceBefore := int64(1000)
	balanceAfter := int64(1100)
	version := int64(2)
	direction := ledger.Credit
	switch kind {
	case tx.Bet:
		direction = ledger.Debit
		balanceAfter = 900
	case tx.Loss:
		balanceAfter = balanceBefore
		version = 1
	case tx.Opening:
		balanceBefore = 0
		balanceAfter = 100
		version = 1
	}
	ref := ""
	if kind == tx.Refund || kind == tx.Rollback {
		ref = referenceID
	}
	after, err := before.MarkProcessed(tx.Result{Balance: cash(t, balanceAfter), WalletVersion: version}, ref, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if kind == tx.Loss {
		return before, after, nil
	}
	entry, err := ledger.New(ledger.State{ID: ledgerID, WalletID: walletID, TransactionID: transactionID, Direction: direction, Money: cash(t, 100), BalanceBefore: cash(t, balanceBefore), BalanceAfter: cash(t, balanceAfter), CreatedAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	return before, after, &entry
}
func object(t *testing.T, event events.Event) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}
func field(t *testing.T, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	var text string
	if err := json.Unmarshal(obj[key], &text); err != nil {
		t.Fatal(err)
	}
	return text
}

func TestProcessedContracts(t *testing.T) {
	for _, kind := range []tx.Kind{tx.Bet, tx.Win, tx.Loss, tx.Refund, tx.Rollback, tx.Opening} {
		t.Run(string(kind), func(t *testing.T) {
			before, after, entry := processed(t, kind)
			batch, err := events.ForTransition(before, after, entry, metadata())
			if err != nil {
				t.Fatal(err)
			}
			count := 2
			if kind == tx.Loss {
				count = 1
			}
			if len(batch) != count {
				t.Fatalf("event count %d", len(batch))
			}
			if _, ok := batch[0].(events.WagerTransactionProcessed); !ok {
				t.Fatal("wrong concrete event")
			}
			for _, event := range batch {
				obj := object(t, event)
				if event.AggregateID() != walletID || field(t, obj, "aggregateId") != walletID || field(t, obj, "correlationId") != "request-123" || field(t, obj, "causationId") != "message-123" {
					t.Fatal("wrong metadata")
				}
				if string(obj["version"]) != "1" || field(t, obj, "occurredAt") != "2026-10-06T01:00:01Z" {
					t.Fatal("wrong version or UTC timestamp")
				}
				if event.ID() != field(t, obj, "eventId") || event.Type() != field(t, obj, "eventType") {
					t.Fatal("metadata accessors differ from JSON")
				}
			}
			obj := object(t, batch[0])
			var data map[string]json.RawMessage
			if err := json.Unmarshal(obj["data"], &data); err != nil {
				t.Fatal(err)
			}
			if field(t, data, "transactionId") != transactionID || field(t, data, "kind") != string(kind) {
				t.Fatal("wrong transaction payload")
			}
			if kind == tx.Opening {
				if field(t, data, "origin") != "INTERNAL" || data["external"] != nil {
					t.Fatal("opening contains external metadata")
				}
			} else if field(t, data, "origin") != "EXTERNAL" || data["external"] == nil {
				t.Fatal("missing external metadata")
			}
			if count == 2 {
				if _, ok := batch[1].(events.WalletBalanceChanged); !ok {
					t.Fatal("wrong balance concrete type")
				}
				if batch[1].Type() != events.BalanceChangedType || batch[1].ID() != balanceEventID {
					t.Fatal("wrong balance envelope")
				}
				balanceObj := object(t, batch[1])
				var balance map[string]json.RawMessage
				if err := json.Unmarshal(balanceObj["data"], &balance); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
					if balance[key] == nil {
						t.Fatalf("missing %s", key)
					}
				}
				if string(balance["money"]) != `{"amount":"1.00","currency":"BRL"}` {
					t.Fatal("money must use exact decimal strings")
				}
				state, err := entry.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(state.BalanceAfter)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(encoded, balance["balanceAfter"]) {
					t.Fatal("balance snapshot differs")
				}
			}
		})
	}
}

func TestRejectionPendingAndFailure(t *testing.T) {
	before := pending(t, tx.Bet)
	for _, withResult := range []bool{false, true} {
		var result *tx.Result
		code := tx.WalletNotFound
		if withResult {
			result = &tx.Result{Balance: cash(t, 50), WalletVersion: 1}
			code = tx.InsufficientFunds
		}
		after, err := before.Reject(code, result, "", now)
		if err != nil {
			t.Fatal(err)
		}
		batch, err := events.ForTransition(before, after, nil, metadata())
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) != 1 {
			t.Fatal("wrong rejection count")
		}
		if _, ok := batch[0].(events.WagerTransactionRejected); !ok {
			t.Fatal("wrong rejection type")
		}
		obj := object(t, batch[0])
		var data map[string]json.RawMessage
		if err := json.Unmarshal(obj["data"], &data); err != nil {
			t.Fatal(err)
		}
		if field(t, data, "failureCode") != string(code) || (data["result"] != nil) != withResult {
			t.Fatal("wrong rejection result")
		}
	}
	before = pending(t, tx.Refund)
	waiting, err := before.WaitForReference(now)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := events.ForTransition(before, waiting, nil, metadata())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].Type() != events.PendingReferenceType {
		t.Fatal("missing pending event")
	}
	if _, ok := batch[0].(events.WagerTransactionPendingReference); !ok {
		t.Fatal("wrong pending concrete type")
	}
	batch, err = events.ForTransition(waiting, waiting, nil, metadata())
	if err != nil || len(batch) != 0 {
		t.Fatal("retry emitted another logical event")
	}
	expired, err := waiting.Reject(tx.ReferenceNotFound, nil, "", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	batch, err = events.ForTransition(waiting, expired, nil, metadata())
	if err != nil || len(batch) != 1 || batch[0].Type() != events.RejectedType {
		t.Fatal("expiration event missing")
	}
	failed, err := before.FailPermanent(now)
	if err != nil {
		t.Fatal(err)
	}
	batch, err = events.ForTransition(before, failed, nil, metadata())
	if err != nil || len(batch) != 0 {
		t.Fatal("unexpected FAILED event")
	}
}

func TestImmutableSerialization(t *testing.T) {
	before, after, entry := processed(t, tx.Win)
	meta := metadata()
	batch, err := events.ForTransition(before, after, entry, meta)
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(batch[0])
	if err != nil {
		t.Fatal(err)
	}
	meta.CorrelationID = "changed"
	state, err := after.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	state.Result.Balance = cash(t, 99999)
	entryState, err := entry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	entryState.BalanceAfter = cash(t, 99999)
	second, err := json.Marshal(batch[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("snapshot changed")
	}
	first[0] = '!'
	third, err := json.Marshal(batch[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, third) {
		t.Fatal("serialized bytes alias event state")
	}
	meta = metadata()
	meta.CausationID = ""
	batch, err = events.ForTransition(before, after, entry, meta)
	if err != nil {
		t.Fatal(err)
	}
	if object(t, batch[0])["causationId"] != nil {
		t.Fatal("absent causation must be omitted")
	}
}

func TestInvalidMetadata(t *testing.T) {
	before, after, entry := processed(t, tx.Bet)
	for _, mutate := range []func(*events.Metadata){
		func(m *events.Metadata) { m.TransactionEventID = "" },
		func(m *events.Metadata) { m.BalanceEventID = "bad" },
		func(m *events.Metadata) { m.BalanceEventID = m.TransactionEventID },
		func(m *events.Metadata) { m.CorrelationID = "" },
		func(m *events.Metadata) { m.CorrelationID = " request" },
		func(m *events.Metadata) { m.CausationID = "line\nbreak" },
	} {
		m := metadata()
		mutate(&m)
		if _, err := events.ForTransition(before, after, entry, m); !errors.Is(err, events.ErrInvalidMetadata) {
			t.Fatal(err)
		}
	}
}

func TestInvalidTransitionsAndLedgers(t *testing.T) {
	before, after, entry := processed(t, tx.Bet)
	if _, err := events.ForTransition(after, after, entry, metadata()); !errors.Is(err, events.ErrInvalidTransition) {
		t.Fatal(err)
	}
	if _, err := events.ForTransition(before, after, nil, metadata()); !errors.Is(err, events.ErrInvalidLedger) {
		t.Fatal(err)
	}
	otherBefore, otherAfter, _ := processed(t, tx.Win)
	if _, err := events.ForTransition(before, otherAfter, entry, metadata()); !errors.Is(err, events.ErrInvalidTransition) {
		t.Fatal(err)
	}
	if _, err := events.ForTransition(otherBefore, otherAfter, entry, metadata()); !errors.Is(err, events.ErrInvalidLedger) {
		t.Fatal(err)
	}
	lossBefore, lossAfter, _ := processed(t, tx.Loss)
	if _, err := events.ForTransition(lossBefore, lossAfter, entry, metadata()); !errors.Is(err, events.ErrInvalidLedger) {
		t.Fatal(err)
	}
	rejected, err := before.Reject(tx.InsufficientFunds, nil, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := events.ForTransition(before, rejected, entry, metadata()); !errors.Is(err, events.ErrInvalidLedger) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ledger.State){
		func(s *ledger.State) { s.WalletID = referenceID },
		func(s *ledger.State) { s.TransactionID = referenceID },
		func(s *ledger.State) { s.CreatedAt = now },
		func(s *ledger.State) { s.Money = cash(t, 200); s.BalanceAfter = cash(t, 800) },
		func(s *ledger.State) { s.BalanceBefore = cash(t, 2000); s.BalanceAfter = cash(t, 1900) },
	} {
		state, err := entry.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		mutate(&state)
		invalid, err := ledger.New(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := events.ForTransition(before, after, &invalid, metadata()); !errors.Is(err, events.ErrInvalidLedger) {
			t.Fatal(err)
		}
	}
	var invalid tx.Transaction
	if _, err := events.ForTransition(invalid, after, entry, metadata()); err == nil {
		t.Fatal("zero transaction accepted")
	}
	for _, event := range []events.Event{events.WagerTransactionProcessed{}, events.WagerTransactionRejected{}, events.WagerTransactionPendingReference{}, events.WalletBalanceChanged{}} {
		if _, err := json.Marshal(event); !errors.Is(err, events.ErrInvalidEvent) {
			t.Fatal(err)
		}
	}
}
