package processing_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/processing"
	tx "github.com/Gregory-Pattrick/go-wagering-service/internal/domain/transaction"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

const operationID = "0192f291-27dd-7d3f-8071-5f8685deef31"
const walletID = "0192f291-27dd-7d3f-8071-5f8685deef32"
const playerID = "0192f291-27dd-7d3f-8071-5f8685deef33"
const ledgerID = "0192f291-27dd-7d3f-8071-5f8685deef34"
const referenceID = "0192f291-27dd-7d3f-8071-5f8685deef35"
const originalLedgerID = "0192f291-27dd-7d3f-8071-5f8685deef36"
const earlierID = "0192f291-27dd-7d3f-8071-5f8685deef37"

var now = time.Date(2026, 10, 5, 22, 0, 0, 0, time.UTC)

func cash(t *testing.T, n int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(n, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func input(t *testing.T, kind tx.Kind, n int64) tx.ExternalInput {
	t.Helper()
	i := tx.ExternalInput{ID: operationID, ExternalTransactionID: "operation", ProviderID: "provider-a", IdempotencyKey: "key-operation", WalletID: walletID, PlayerID: playerID, RoundID: "round", GameID: "game", Kind: kind, Money: cash(t, n)}
	if kind == tx.Refund || kind == tx.Rollback {
		i.ReferenceExternalTransactionID = "original"
	}
	return i
}
func transaction(t *testing.T, i tx.ExternalInput) tx.Transaction {
	t.Helper()
	tr, err := tx.NewExternal(i, now)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func request(t *testing.T, kind tx.Kind, n, balance int64) processing.Request {
	t.Helper()
	w, err := wallet.New(walletID, playerID, cash(t, balance), now)
	if err != nil {
		t.Fatal(err)
	}
	return processing.Request{Transaction: transaction(t, input(t, kind, n)), Wallet: w, LedgerID: ledgerID, Now: now.Add(time.Second)}
}
func txState(t *testing.T, tr tx.Transaction) tx.State {
	t.Helper()
	s, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func walletState(t *testing.T, w wallet.Wallet) wallet.State {
	t.Helper()
	s, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func referenceInput(t *testing.T, kind tx.Kind, n int64) tx.ExternalInput {
	i := input(t, kind, n)
	i.ID = referenceID
	i.ExternalTransactionID = "original"
	i.IdempotencyKey = "key-original"
	if kind == tx.Refund || kind == tx.Rollback {
		i.ReferenceExternalTransactionID = "earlier"
	}
	return i
}
func reference(t *testing.T, kind tx.Kind, n int64) *processing.Reference {
	t.Helper()
	i := referenceInput(t, kind, n)
	pending := transaction(t, i)
	direction := ledger.Credit
	before := int64(10000)
	after := before + n
	if kind == tx.Bet {
		direction = ledger.Debit
		after = before - n
	}
	if kind == tx.Loss {
		after = before
	}
	resolved := ""
	if i.ReferenceExternalTransactionID != "" {
		resolved = earlierID
	}
	completed, err := pending.MarkProcessed(tx.Result{Balance: cash(t, after), WalletVersion: 2}, resolved, now)
	if err != nil {
		t.Fatal(err)
	}
	ref := &processing.Reference{Transaction: completed}
	if kind != tx.Loss {
		entry, err := ledger.New(ledger.State{ID: originalLedgerID, WalletID: walletID, TransactionID: referenceID, Direction: direction, Money: cash(t, n), BalanceBefore: cash(t, before), BalanceAfter: cash(t, after), CreatedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		ref.Ledger = &entry
	}
	return ref
}
func assertDecision(t *testing.T, d processing.Decision, status tx.Status, code tx.FailureCode, balance, version int64, hasEntry bool) {
	t.Helper()
	s := txState(t, d.Transaction)
	w := walletState(t, d.Wallet)
	cents, _ := w.Balance.MinorUnits()
	if s.Status != status || s.FailureCode != code || cents != balance || w.Version != version || (d.Entry != nil) != hasEntry {
		t.Fatalf("status=%s code=%s balance=%d version=%d entry=%v", s.Status, s.FailureCode, cents, w.Version, d.Entry != nil)
	}
	if status == tx.Processed || (status == tx.Rejected && s.Result != nil) {
		if s.Result == nil {
			t.Fatal("missing original result")
		}
		result, _ := s.Result.Balance.MinorUnits()
		if result != balance || s.Result.WalletVersion != version {
			t.Fatal("incorrect result snapshot")
		}
	}
}

func TestFinancialEffects(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		kind                  tx.Kind
		amount, before, after int64
		refKind               tx.Kind
		direction             ledger.Direction
	}{
		{"bet", tx.Bet, 8000, 10000, 2000, "", ledger.Debit},
		{"win", tx.Win, 3000, 2000, 5000, "", ledger.Credit},
		{"refund bet", tx.Refund, 8000, 2000, 10000, tx.Bet, ledger.Credit},
		{"rollback bet", tx.Rollback, 8000, 2000, 10000, tx.Bet, ledger.Credit},
		{"rollback win", tx.Rollback, 3000, 5000, 2000, tx.Win, ledger.Debit},
		{"rollback refund", tx.Rollback, 8000, 10000, 2000, tx.Refund, ledger.Debit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := request(t, tc.kind, tc.amount, tc.before)
			if tc.refKind != "" {
				req.Reference = reference(t, tc.refKind, tc.amount)
			}
			before := walletState(t, req.Wallet)
			d, err := processing.Evaluate(req)
			if err != nil {
				t.Fatal(err)
			}
			assertDecision(t, d, tx.Processed, "", tc.after, 2, true)
			e, err := d.Entry.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if e.Direction != tc.direction || e.TransactionID != operationID || e.WalletID != walletID {
				t.Fatal("incorrect ledger identity or direction")
			}
			amount, _ := e.Money.MinorUnits()
			initial, _ := e.BalanceBefore.MinorUnits()
			final, _ := e.BalanceAfter.MinorUnits()
			if amount != tc.amount || initial != tc.before || final != tc.after {
				t.Fatal("incorrect ledger amounts")
			}
			if walletState(t, req.Wallet) != before || txState(t, req.Transaction).Status != tx.Pending {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestLossAndWinReference(t *testing.T) {
	loss := request(t, tx.Loss, 0, 10000)
	loss.LedgerID = "" // LOSS never needs or creates a ledger identity.
	d, err := processing.Evaluate(loss)
	if err != nil {
		t.Fatal(err)
	}
	assertDecision(t, d, tx.Processed, "", 10000, 1, false)
	req := request(t, tx.Win, 3000, 2000)
	i := input(t, tx.Win, 3000)
	i.ReferenceExternalTransactionID = "original"
	i.GameID = "another-game"
	req.Transaction = transaction(t, i)
	req.Reference = reference(t, tx.Bet, 8000)
	req.Reference.Compensated = true // A WIN is not a second reversal.
	d, err = processing.Evaluate(req)
	if err != nil {
		t.Fatal(err)
	}
	assertDecision(t, d, tx.Processed, "", 5000, 2, true)
	if txState(t, d.Transaction).ResolvedReferenceID != referenceID {
		t.Fatal("reference was not retained")
	}
}

func TestBusinessRejections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     processing.Request
		code    tx.FailureCode
		balance int64
	}{
		{"bet insufficient", request(t, tx.Bet, 8000, 2000), tx.InsufficientFunds, 2000},
		{"credit overflow", request(t, tx.Win, 1, math.MaxInt64), tx.BalanceOverflow, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := processing.Evaluate(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			assertDecision(t, d, tx.Rejected, tc.code, tc.balance, 1, false)
		})
	}
	for _, kind := range []tx.Kind{tx.Win, tx.Refund} {
		req := request(t, tx.Rollback, 8000, 2000)
		req.Reference = reference(t, kind, 8000)
		d, err := processing.Evaluate(req)
		if err != nil {
			t.Fatal(err)
		}
		assertDecision(t, d, tx.Rejected, tx.ReversalInsufficientFunds, 2000, 1, false)
	}
	for _, kind := range []tx.Kind{tx.Refund, tx.Rollback} {
		req := request(t, kind, 8000, 2000)
		req.Reference = reference(t, tx.Bet, 8000)
		req.Reference.Compensated = true
		d, err := processing.Evaluate(req)
		if err != nil {
			t.Fatal(err)
		}
		assertDecision(t, d, tx.Rejected, tx.AlreadyReversed, 2000, 1, false)
	}
}

func TestMissingAndPendingReferences(t *testing.T) {
	for _, kind := range []tx.Kind{tx.Win, tx.Refund, tx.Rollback} {
		for _, refStatus := range []tx.Status{"", tx.Pending, tx.PendingReference} {
			req := request(t, kind, 100, 1000)
			i := input(t, kind, 100)
			i.ReferenceExternalTransactionID = "original"
			req.Transaction = transaction(t, i)
			refKind := tx.Bet
			if refStatus != "" {
				if refStatus == tx.PendingReference {
					// Only ROLLBACK accepts a REFUND, which can itself be waiting.
					if kind != tx.Rollback {
						continue
					}
					refKind = tx.Refund
				}
				pending := transaction(t, referenceInput(t, refKind, 100))
				if refStatus == tx.PendingReference {
					var err error
					pending, err = pending.WaitForReference(now)
					if err != nil {
						t.Fatal(err)
					}
				}
				req.Reference = &processing.Reference{Transaction: pending}
			}
			d, err := processing.Evaluate(req)
			if err != nil {
				t.Fatal(err)
			}
			assertDecision(t, d, tx.PendingReference, "", 1000, 1, false)
			req.Transaction = d.Transaction
			again, err := processing.Evaluate(req)
			if err != nil {
				t.Fatal(err)
			}
			assertDecision(t, again, tx.PendingReference, "", 1000, 1, false)
			req.Reference = reference(t, refKind, 100)
			resumed, err := processing.Evaluate(req)
			if err != nil {
				t.Fatal(err)
			}
			expected := int64(1100)
			if refKind == tx.Refund {
				expected = 900
			}
			assertDecision(t, resumed, tx.Processed, "", expected, 2, true)
		}
	}
}

func TestUnsuccessfulAndIneligibleReferences(t *testing.T) {
	for _, failed := range []bool{false, true} {
		req := request(t, tx.Refund, 100, 1000)
		pending := transaction(t, referenceInput(t, tx.Bet, 100))
		var original tx.Transaction
		var err error
		if failed {
			original, err = pending.FailPermanent(now)
		} else {
			original, err = pending.Reject(tx.InsufficientFunds, nil, "", now)
		}
		if err != nil {
			t.Fatal(err)
		}
		req.Reference = &processing.Reference{Transaction: original}
		d, err := processing.Evaluate(req)
		if err != nil {
			t.Fatal(err)
		}
		assertDecision(t, d, tx.Rejected, tx.ReferenceNotProcessed, 1000, 1, false)
	}
	for _, tc := range []struct {
		kind, refKind tx.Kind
		refAmount     int64
	}{
		{tx.Refund, tx.Win, 100}, {tx.Refund, tx.Refund, 100}, {tx.Rollback, tx.Rollback, 100}, {tx.Rollback, tx.Loss, 0},
	} {
		req := request(t, tc.kind, 100, 1000)
		req.Reference = reference(t, tc.refKind, tc.refAmount)
		d, err := processing.Evaluate(req)
		if err != nil {
			t.Fatal(err)
		}
		assertDecision(t, d, tx.Rejected, tx.ReferenceKindInvalid, 1000, 1, false)
	}
	req := request(t, tx.Refund, 100, 1000)
	req.Reference = reference(t, tx.Bet, 200)
	d, err := processing.Evaluate(req)
	if err != nil {
		t.Fatal(err)
	}
	assertDecision(t, d, tx.Rejected, tx.ReferenceMismatch, 1000, 1, false)
}

func TestReferenceContextMismatch(t *testing.T) {
	for _, mutate := range []func(*tx.ExternalInput){
		func(i *tx.ExternalInput) { i.ProviderID = "provider-b" },
		func(i *tx.ExternalInput) { i.ExternalTransactionID = "wrong-original" },
		func(i *tx.ExternalInput) { i.PlayerID = earlierID },
		func(i *tx.ExternalInput) { i.WalletID = earlierID },
		func(i *tx.ExternalInput) { i.RoundID = "other-round" },
		func(i *tx.ExternalInput) { i.ID = operationID },
		func(i *tx.ExternalInput) {
			var err error
			i.Money, err = money.FromMinor(100, money.USD)
			if err != nil {
				t.Fatal(err)
			}
		},
	} {
		req := request(t, tx.Refund, 100, 1000)
		i := referenceInput(t, tx.Bet, 100)
		mutate(&i)
		req.Reference = &processing.Reference{Transaction: transaction(t, i)}
		d, err := processing.Evaluate(req)
		if err != nil {
			t.Fatal(err)
		}
		assertDecision(t, d, tx.Rejected, tx.ReferenceMismatch, 1000, 1, false)
	}
}

func TestWalletMismatchDoesNotExposeBalance(t *testing.T) {
	for _, mutate := range []func(*tx.ExternalInput){
		func(i *tx.ExternalInput) { i.WalletID = earlierID },
		func(i *tx.ExternalInput) { i.PlayerID = earlierID },
		func(i *tx.ExternalInput) {
			var err error
			i.Money, err = money.FromMinor(100, money.USD)
			if err != nil {
				t.Fatal(err)
			}
		},
	} {
		req := request(t, tx.Bet, 100, 1000)
		i := input(t, tx.Bet, 100)
		mutate(&i)
		req.Transaction = transaction(t, i)
		d, err := processing.Evaluate(req)
		if err != nil {
			t.Fatal(err)
		}
		assertDecision(t, d, tx.Rejected, tx.WalletMismatch, 1000, 1, false)
		if txState(t, d.Transaction).Result != nil {
			t.Fatal("mismatched wallet balance disclosed")
		}
	}
}

func TestInvalidReferenceLedger(t *testing.T) {
	for _, mutate := range []func(*ledger.State){
		func(s *ledger.State) { s.TransactionID = earlierID },
		func(s *ledger.State) { s.WalletID = earlierID },
		func(s *ledger.State) { s.Direction = ledger.Credit; s.BalanceAfter = cash(t, 10100) },
		func(s *ledger.State) { s.Money = cash(t, 200); s.BalanceAfter = cash(t, 9800) },
		func(s *ledger.State) { s.BalanceBefore = cash(t, 20000); s.BalanceAfter = cash(t, 19900) },
	} {
		req := request(t, tx.Refund, 100, 1000)
		req.Reference = reference(t, tx.Bet, 100)
		s, err := req.Reference.Ledger.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		mutate(&s)
		entry, err := ledger.New(s)
		if err != nil {
			t.Fatal(err)
		}
		req.Reference.Ledger = &entry
		if _, err := processing.Evaluate(req); !errors.Is(err, processing.ErrInvalidReferenceRecord) {
			t.Fatal(err)
		}
	}
	req := request(t, tx.Refund, 100, 1000)
	req.Reference = reference(t, tx.Bet, 100)
	req.Reference.Ledger = nil
	if _, err := processing.Evaluate(req); !errors.Is(err, processing.ErrInvalidReferenceRecord) {
		t.Fatal(err)
	}
}

func TestErrorsAndTerminalReplay(t *testing.T) {
	req := request(t, tx.Bet, 100, 1000)
	completed, err := processing.Evaluate(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Transaction = completed.Transaction
	if _, err := processing.Evaluate(req); !errors.Is(err, processing.ErrTerminalTransaction) {
		t.Fatal(err)
	}
	opening, err := tx.NewOpening(tx.OpeningInput{ID: operationID, WalletID: walletID, PlayerID: playerID, Money: cash(t, 1000)}, now)
	if err != nil {
		t.Fatal(err)
	}
	req.Transaction = opening
	if _, err := processing.Evaluate(req); !errors.Is(err, processing.ErrInternalOperation) {
		t.Fatal(err)
	}
	req = request(t, tx.Bet, 100, 1000)
	req.Reference = reference(t, tx.Bet, 100)
	if _, err := processing.Evaluate(req); !errors.Is(err, processing.ErrUnexpectedReference) {
		t.Fatal(err)
	}
	req = request(t, tx.Bet, 100, 1000)
	req.LedgerID = ""
	if _, err := processing.Evaluate(req); !errors.Is(err, ledger.ErrInvalidIdentity) {
		t.Fatal(err)
	}
	if walletState(t, req.Wallet).Version != 1 || txState(t, req.Transaction).Status != tx.Pending {
		t.Fatal("failed evaluation changed input")
	}
	req = request(t, tx.Bet, 100, 1000)
	req.Now = time.Time{}
	if _, err := processing.Evaluate(req); !errors.Is(err, tx.ErrInvalidTime) {
		t.Fatal(err)
	}
	if _, err := processing.Evaluate(processing.Request{}); err == nil {
		t.Fatal("zero request accepted")
	}
	req = request(t, tx.Bet, 100, 1000)
	s := walletState(t, req.Wallet)
	s.Version = math.MaxInt64
	req.Wallet, err = wallet.Rehydrate(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processing.Evaluate(req); !errors.Is(err, wallet.ErrVersionOverflow) {
		t.Fatal(err)
	}
}

func TestOriginalResultSurvivesLaterMovement(t *testing.T) {
	req := request(t, tx.Bet, 8000, 10000)
	bet, err := processing.Evaluate(req)
	if err != nil {
		t.Fatal(err)
	}
	winReq := request(t, tx.Win, 3000, 0)
	winReq.Wallet = bet.Wallet
	winReq.LedgerID = earlierID
	i := input(t, tx.Win, 3000)
	i.ID = earlierID
	i.ExternalTransactionID = "later-win"
	i.IdempotencyKey = "later-key"
	winReq.Transaction = transaction(t, i)
	win, err := processing.Evaluate(winReq)
	if err != nil {
		t.Fatal(err)
	}
	assertDecision(t, win, tx.Processed, "", 5000, 3, true)
	original := txState(t, bet.Transaction)
	cents, _ := original.Result.Balance.MinorUnits()
	if cents != 2000 || original.Result.WalletVersion != 2 {
		t.Fatal("original result changed after later movement")
	}
}

func TestCompensationHistoryIsNotReopened(t *testing.T) {
	for _, firstKind := range []tx.Kind{tx.Refund, tx.Rollback} {
		for _, secondKind := range []tx.Kind{tx.Refund, tx.Rollback} {
			original := reference(t, tx.Bet, 8000)
			req := request(t, firstKind, 8000, 2000)
			req.Reference = original
			first, err := processing.Evaluate(req)
			if err != nil {
				t.Fatal(err)
			}
			assertDecision(t, first, tx.Processed, "", 10000, 2, true)
			// The SQL adapter must derive this flag from committed compensation history.
			original.Compensated = true
			secondReq := request(t, secondKind, 8000, 0)
			secondReq.Wallet = first.Wallet
			secondReq.Reference = original
			secondInput := input(t, secondKind, 8000)
			secondInput.ID = earlierID
			secondInput.ExternalTransactionID = "second-reversal"
			secondInput.IdempotencyKey = "second-key"
			secondReq.Transaction = transaction(t, secondInput)
			second, err := processing.Evaluate(secondReq)
			if err != nil {
				t.Fatal(err)
			}
			assertDecision(t, second, tx.Rejected, tx.AlreadyReversed, 10000, 2, false)
			if firstKind == tx.Refund {
				rollbackReq := request(t, tx.Rollback, 8000, 0)
				rollbackReq.Wallet = first.Wallet
				rollbackReq.LedgerID = earlierID
				rollbackInput := input(t, tx.Rollback, 8000)
				rollbackInput.ID = earlierID
				rollbackInput.ExternalTransactionID = "rollback-refund"
				rollbackInput.IdempotencyKey = "rollback-key"
				rollbackInput.ReferenceExternalTransactionID = "operation"
				rollbackReq.Transaction = transaction(t, rollbackInput)
				rollbackReq.Reference = &processing.Reference{Transaction: first.Transaction, Ledger: first.Entry}
				undone, err := processing.Evaluate(rollbackReq)
				if err != nil {
					t.Fatal(err)
				}
				assertDecision(t, undone, tx.Processed, "", 2000, 3, true)
				secondReq.Wallet = undone.Wallet
				second, err = processing.Evaluate(secondReq)
				if err != nil {
					t.Fatal(err)
				}
				assertDecision(t, second, tx.Rejected, tx.AlreadyReversed, 2000, 3, false)
			}
		}
	}
}

func TestSequentialBetsAndOccurrenceTime(t *testing.T) {
	req := request(t, tx.Bet, 8000, 10000)
	req.Now = now.Add(-time.Hour)
	first, err := processing.Evaluate(req)
	if err != nil {
		t.Fatal(err)
	}
	assertDecision(t, first, tx.Processed, "", 2000, 2, true)
	entry, err := first.Entry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !entry.CreatedAt.Equal(now) || !walletState(t, first.Wallet).UpdatedAt.Equal(now) || !txState(t, first.Transaction).UpdatedAt.Equal(now) {
		t.Fatal("inconsistent occurrence times")
	}
	secondReq := request(t, tx.Bet, 8000, 0)
	secondReq.Wallet = first.Wallet
	i := input(t, tx.Bet, 8000)
	i.ID = earlierID
	i.ExternalTransactionID = "second-bet"
	i.IdempotencyKey = "second-key"
	secondReq.Transaction = transaction(t, i)
	second, err := processing.Evaluate(secondReq)
	if err != nil {
		t.Fatal(err)
	}
	assertDecision(t, second, tx.Rejected, tx.InsufficientFunds, 2000, 2, false)
	// This sequential test does not replace the required concurrent SQL test.
}
