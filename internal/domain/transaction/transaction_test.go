package transaction_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
)

const transactionID = "0192f291-27dd-7d3f-8071-5f8685deef37"
const walletID = "0192f291-27dd-7d3f-8071-5f8685deef38"
const playerID = "0192f291-27dd-7d3f-8071-5f8685deef39"
const referenceID = "0192f291-27dd-7d3f-8071-5f8685deef40"

var now = time.Date(2026, 10, 5, 21, 0, 0, 0, time.UTC)

func value(t *testing.T, cents int64, currency money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(cents, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func input(t *testing.T, kind tx.Kind) tx.ExternalInput {
	t.Helper()
	cents := int64(2500)
	if kind == tx.Loss {
		cents = 0
	}
	i := tx.ExternalInput{ID: transactionID, ExternalTransactionID: "external-1", ProviderID: "provider-a", IdempotencyKey: "key-1", WalletID: walletID, PlayerID: playerID, RoundID: "round-1", GameID: "game-1", Kind: kind, Money: value(t, cents, money.BRL)}
	if kind == tx.Refund || kind == tx.Rollback {
		i.ReferenceExternalTransactionID = "original-1"
	}
	return i
}
func create(t *testing.T, kind tx.Kind) tx.Transaction {
	t.Helper()
	tr, err := tx.NewExternal(input(t, kind), now)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func snapshot(t *testing.T, tr tx.Transaction) tx.State {
	t.Helper()
	s, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func result(t *testing.T) tx.Result {
	return tx.Result{Balance: value(t, 7500, money.BRL), WalletVersion: 2}
}

func TestKindsAndAmounts(t *testing.T) {
	for _, kind := range []tx.Kind{tx.Bet, tx.Win, tx.Loss, tx.Refund, tx.Rollback} {
		tr := create(t, kind)
		s := snapshot(t, tr)
		if s.Status != tx.Pending || s.Input.Kind != kind || s.Result != nil || len(s.PayloadHash) != 64 {
			t.Fatalf("bad initial state: %+v", s)
		}
		for _, cents := range []int64{-1, 0, 1} {
			i := input(t, kind)
			i.Money = value(t, cents, money.BRL)
			_, err := tx.NewExternal(i, now)
			valid := (kind == tx.Loss && cents == 0) || (kind != tx.Loss && cents > 0)
			if valid && err != nil {
				t.Fatal(err)
			}
			if !valid && !errors.Is(err, tx.ErrInvalidAmount) {
				t.Fatalf("kind=%s cents=%d: %v", kind, cents, err)
			}
		}
	}
	for _, kind := range []tx.Kind{tx.Opening, "UNKNOWN", "bet", ""} {
		if _, err := tx.NewExternal(input(t, kind), now); !errors.Is(err, tx.ErrInvalidKind) {
			t.Fatal(err)
		}
	}
}

func TestExternalValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*tx.ExternalInput)
		want   error
	}{
		{"ID", func(i *tx.ExternalInput) { i.ID = "" }, tx.ErrInvalidIdentity},
		{"nil UUID", func(i *tx.ExternalInput) { i.ID = "00000000-0000-0000-0000-000000000000" }, tx.ErrInvalidIdentity},
		{"uppercase UUID", func(i *tx.ExternalInput) { i.ID = "0192F291-27DD-7D3F-8071-5F8685DEEF37" }, tx.ErrInvalidIdentity},
		{"wallet", func(i *tx.ExternalInput) { i.WalletID = "invalid" }, tx.ErrInvalidIdentity},
		{"player", func(i *tx.ExternalInput) { i.PlayerID = "" }, tx.ErrInvalidIdentity},
		{"external", func(i *tx.ExternalInput) { i.ExternalTransactionID = "" }, tx.ErrInvalidIdentity},
		{"provider", func(i *tx.ExternalInput) { i.ProviderID = "" }, tx.ErrInvalidIdentity},
		{"key", func(i *tx.ExternalInput) { i.IdempotencyKey = " " }, tx.ErrInvalidIdentity},
		{"round", func(i *tx.ExternalInput) { i.RoundID = "round\n" }, tx.ErrInvalidIdentity},
		{"game", func(i *tx.ExternalInput) { i.GameID = "" }, tx.ErrInvalidIdentity},
		{"invalid UTF8", func(i *tx.ExternalInput) { i.GameID = string([]byte{255}) }, tx.ErrInvalidIdentity},
		{"money", func(i *tx.ExternalInput) { i.Money = money.Money{} }, money.ErrInvalidMoney},
		{"unexpected reference", func(i *tx.ExternalInput) { i.ReferenceExternalTransactionID = "original" }, tx.ErrInvalidReference},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := input(t, tx.Bet)
			tc.mutate(&i)
			if _, err := tx.NewExternal(i, now); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	for _, kind := range []tx.Kind{tx.Refund, tx.Rollback} {
		i := input(t, kind)
		i.ReferenceExternalTransactionID = ""
		if _, err := tx.NewExternal(i, now); !errors.Is(err, tx.ErrInvalidReference) {
			t.Fatal(err)
		}
	}
	i := input(t, tx.Win)
	i.ReferenceExternalTransactionID = i.ExternalTransactionID
	if _, err := tx.NewExternal(i, now); !errors.Is(err, tx.ErrInvalidReference) {
		t.Fatal(err)
	}
	i.ReferenceExternalTransactionID = "original"
	if _, err := tx.NewExternal(i, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.NewExternal(input(t, tx.Bet), time.Time{}); !errors.Is(err, tx.ErrInvalidTime) {
		t.Fatal(err)
	}
}

func TestHashCanonicalizationAndIdentity(t *testing.T) {
	base := input(t, tx.Win)
	initial, err := tx.NewExternal(base, now)
	if err != nil {
		t.Fatal(err)
	}
	original := snapshot(t, initial).PayloadHash
	// An independently assembled map is sorted recursively by encoding/json.
	canonical := map[string]any{"externalTransactionId": base.ExternalTransactionID, "gameId": base.GameID, "kind": string(base.Kind), "money": map[string]string{"amount": "25.00", "currency": "BRL"}, "playerId": base.PlayerID, "providerId": base.ProviderID, "referenceExternalTransactionId": "", "roundId": base.RoundID, "walletId": base.WalletID}
	bytes, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes)
	if original != hex.EncodeToString(sum[:]) {
		t.Fatal("hash differs from canonical business payload")
	}
	changed := base
	changed.ID = referenceID
	changed.IdempotencyKey = "alternative-key"
	other, err := tx.NewExternal(changed, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot(t, other).PayloadHash != original {
		t.Fatal("non-business metadata changed hash")
	}
	for _, mutate := range []func(*tx.ExternalInput){
		func(i *tx.ExternalInput) { i.ExternalTransactionID = "external-2" },
		func(i *tx.ExternalInput) { i.GameID = "game-2" },
		func(i *tx.ExternalInput) { i.Kind = tx.Bet },
		func(i *tx.ExternalInput) { i.Money = value(t, 2501, money.BRL) },
		func(i *tx.ExternalInput) { i.Money = value(t, 2500, money.USD) },
		func(i *tx.ExternalInput) { i.PlayerID = referenceID },
		func(i *tx.ExternalInput) { i.ProviderID = "provider-b" },
		func(i *tx.ExternalInput) { i.ReferenceExternalTransactionID = "original" },
		func(i *tx.ExternalInput) { i.RoundID = "round-2" },
		func(i *tx.ExternalInput) { i.WalletID = referenceID },
	} {
		changed := base
		mutate(&changed)
		other, err := tx.NewExternal(changed, now)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot(t, other).PayloadHash == original {
			t.Fatal("business change did not change hash")
		}
	}
	s := snapshot(t, initial)
	s.Input.Money = value(t, 2501, money.BRL)
	if _, err := tx.Rehydrate(s); !errors.Is(err, tx.ErrInvalidHash) {
		t.Fatal(err)
	}
}

func TestLifecycleAndTerminalProtection(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		original := create(t, tx.Refund)
		active := original
		if waiting {
			var err error
			active, err = active.WaitForReference(now.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot(t, active).Status != tx.PendingReference {
				t.Fatal("not waiting")
			}
			if _, err := active.WaitForReference(now); !errors.Is(err, tx.ErrInvalidTransition) {
				t.Fatal(err)
			}
		}
		completed, err := active.MarkProcessed(result(t), referenceID, now.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		rejected, err := active.Reject(tx.ReferenceNotFound, nil, "", now)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := active.FailPermanent(now)
		if err != nil {
			t.Fatal(err)
		}
		for _, terminal := range []tx.Transaction{completed, rejected, failed} {
			if _, err := terminal.WaitForReference(now); !errors.Is(err, tx.ErrInvalidTransition) {
				t.Fatal(err)
			}
			if _, err := terminal.MarkProcessed(result(t), referenceID, now); !errors.Is(err, tx.ErrInvalidTransition) {
				t.Fatal(err)
			}
			if _, err := terminal.Reject(tx.ReferenceNotFound, nil, "", now); !errors.Is(err, tx.ErrInvalidTransition) {
				t.Fatal(err)
			}
			if _, err := terminal.FailPermanent(now); !errors.Is(err, tx.ErrInvalidTransition) {
				t.Fatal(err)
			}
			restored, err := tx.Rehydrate(snapshot(t, terminal))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot(t, restored).Status != snapshot(t, terminal).Status {
				t.Fatal("rehydration changed status")
			}
		}
		if snapshot(t, original).Status != tx.Pending {
			t.Fatal("original mutated")
		}
	}
	for _, kind := range []tx.Kind{tx.Bet, tx.Win, tx.Loss} {
		if _, err := create(t, kind).WaitForReference(now); !errors.Is(err, tx.ErrInvalidReference) {
			t.Fatal(err)
		}
	}
	winInput := input(t, tx.Win)
	winInput.ReferenceExternalTransactionID = "original"
	win, err := tx.NewExternal(winInput, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := win.WaitForReference(now); err != nil {
		t.Fatal(err)
	}
	if _, err := win.MarkProcessed(result(t), "", now); !errors.Is(err, tx.ErrInvalidReference) {
		t.Fatal(err)
	}
}

func TestResultsAreDetachedAndValidated(t *testing.T) {
	active := create(t, tx.Bet)
	r := result(t)
	rejected, err := active.Reject(tx.InsufficientFunds, &r, "", now)
	if err != nil {
		t.Fatal(err)
	}
	r.WalletVersion = 999
	s := snapshot(t, rejected)
	if s.Result.WalletVersion != 2 {
		t.Fatal("input result retained by reference")
	}
	s.Result.WalletVersion = 888
	if snapshot(t, rejected).Result.WalletVersion != 2 {
		t.Fatal("output result aliases aggregate")
	}
	completed, err := active.MarkProcessed(result(t), "", now)
	if err != nil {
		t.Fatal(err)
	}
	persisted := snapshot(t, completed)
	restored, err := tx.Rehydrate(persisted)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Result.WalletVersion = 777
	if snapshot(t, restored).Result.WalletVersion != 2 {
		t.Fatal("rehydration result aliases aggregate")
	}
	for _, bad := range []tx.Result{{}, {Balance: value(t, -1, money.BRL), WalletVersion: 2}, {Balance: value(t, 1, money.USD), WalletVersion: 2}, {Balance: value(t, 1, money.BRL), WalletVersion: 0}} {
		if _, err := active.MarkProcessed(bad, "", now); !errors.Is(err, tx.ErrInvalidResult) {
			t.Fatal(err)
		}
	}
	if _, err := active.MarkProcessed(result(t), referenceID, now); !errors.Is(err, tx.ErrInvalidReference) {
		t.Fatal(err)
	}
	if _, err := active.Reject(tx.PermanentInfrastructureFailure, nil, "", now); !errors.Is(err, tx.ErrInvalidFailureCode) {
		t.Fatal(err)
	}
	if _, err := active.Reject("UNKNOWN", nil, "", now); !errors.Is(err, tx.ErrInvalidFailureCode) {
		t.Fatal(err)
	}
	if tx.InsufficientFunds == tx.ReversalInsufficientFunds {
		t.Fatal("failure codes must differ")
	}
}

func TestOpening(t *testing.T) {
	i := tx.OpeningInput{ID: transactionID, WalletID: walletID, PlayerID: playerID, Money: value(t, 10000, money.BRL)}
	opening, err := tx.NewOpening(i, now)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot(t, opening)
	if s.PayloadHash != "" || s.Input.ProviderID != "" || s.Input.ExternalTransactionID != "" || s.Input.IdempotencyKey != "" || s.Input.RoundID != "" || s.Input.GameID != "" {
		t.Fatal("opening has external metadata")
	}
	good := tx.Result{Balance: i.Money, WalletVersion: 1}
	if _, err := opening.MarkProcessed(good, "", now); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []tx.Result{{Balance: i.Money, WalletVersion: 2}, {Balance: value(t, 9999, money.BRL), WalletVersion: 1}} {
		if _, err := opening.MarkProcessed(bad, "", now); !errors.Is(err, tx.ErrInvalidResult) {
			t.Fatal(err)
		}
	}
	i.Money = value(t, 0, money.BRL)
	if _, err := tx.NewOpening(i, now); !errors.Is(err, tx.ErrInvalidAmount) {
		t.Fatal(err)
	}
	s.Input.ProviderID = "provider-a"
	if _, err := tx.Rehydrate(s); !errors.Is(err, tx.ErrInvalidIdentity) {
		t.Fatal(err)
	}
}

func TestInvalidPersistedStatesAndZeroValue(t *testing.T) {
	base := snapshot(t, create(t, tx.Bet))
	for _, mutate := range []func(*tx.State){
		func(s *tx.State) { s.Status = "UNKNOWN" },
		func(s *tx.State) { s.Status = tx.Processed },
		func(s *tx.State) { s.Status = tx.Rejected },
		func(s *tx.State) { s.Status = tx.Failed },
		func(s *tx.State) { s.Status = tx.PendingReference },
		func(s *tx.State) { s.Result = &tx.Result{} },
		func(s *tx.State) { s.FailureCode = tx.InsufficientFunds },
		func(s *tx.State) { s.ResolvedReferenceID = referenceID },
		func(s *tx.State) { s.CreatedAt = time.Time{} },
		func(s *tx.State) { s.UpdatedAt = now.Add(-time.Second) },
	} {
		s := base
		mutate(&s)
		if _, err := tx.Rehydrate(s); err == nil {
			t.Fatal("invalid persisted state accepted")
		}
	}
	var invalid tx.Transaction
	if invalid.Validate() == nil {
		t.Fatal("zero value accepted")
	}
	if _, err := invalid.Snapshot(); err == nil {
		t.Fatal("zero snapshot accepted")
	}
	if _, err := invalid.WaitForReference(now); err == nil {
		t.Fatal("zero wait accepted")
	}
	if _, err := invalid.MarkProcessed(result(t), "", now); err == nil {
		t.Fatal("zero processing accepted")
	}
	if _, err := invalid.Reject(tx.WalletNotFound, nil, "", now); err == nil {
		t.Fatal("zero rejection accepted")
	}
	if _, err := invalid.FailPermanent(now); err == nil {
		t.Fatal("zero failure accepted")
	}
	active := create(t, tx.Bet)
	if _, err := active.FailPermanent(time.Time{}); !errors.Is(err, tx.ErrInvalidTime) {
		t.Fatal(err)
	}
	changed, err := active.FailPermanent(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot(t, changed).UpdatedAt.Equal(now) {
		t.Fatal("timestamp regressed")
	}
}
