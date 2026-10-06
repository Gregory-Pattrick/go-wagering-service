package ledger_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/ledger"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
)

const entryID = "0192f291-27dd-7d3f-8071-5f8685deef37"
const walletID = "0192f291-27dd-7d3f-8071-5f8685deef38"
const transactionID = "0192f291-27dd-7d3f-8071-5f8685deef39"

var created = time.Date(2026, 10, 5, 22, 0, 0, 0, time.FixedZone("local", -3*3600))

func amount(t *testing.T, cents int64, currency money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(cents, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func state(t *testing.T, direction ledger.Direction, before, movement, after int64) ledger.State {
	t.Helper()
	return ledger.State{ID: entryID, WalletID: walletID, TransactionID: transactionID, Direction: direction,
		Money: amount(t, movement, money.BRL), BalanceBefore: amount(t, before, money.BRL), BalanceAfter: amount(t, after, money.BRL), CreatedAt: created}
}

func TestValidEntriesAndRehydration(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		direction               ledger.Direction
		before, movement, after int64
	}{
		{"opening credit", ledger.Credit, 0, 10000, 10000},
		{"bet debit", ledger.Debit, 10000, 8000, 2000},
		{"exact depletion", ledger.Debit, 10000, 10000, 0},
		{"one cent credit", ledger.Credit, 10, 1, 11},
		{"one cent debit", ledger.Debit, 11, 1, 10},
		{"maximum opening", ledger.Credit, 0, math.MaxInt64, math.MaxInt64},
		{"maximum resulting balance", ledger.Credit, math.MaxInt64 - 1, 1, math.MaxInt64},
		{"maximum debit", ledger.Debit, math.MaxInt64, math.MaxInt64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := state(t, tc.direction, tc.before, tc.movement, tc.after)
			entry, err := ledger.New(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := entry.Validate(); err != nil {
				t.Fatal(err)
			}
			snapshot, err := entry.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			input.CreatedAt = input.CreatedAt.UTC()
			if snapshot != input {
				t.Fatalf("snapshot differs from input: %+v", snapshot)
			}
			restored, err := ledger.Rehydrate(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			again, err := restored.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if again != snapshot {
				t.Fatal("rehydration changed the financial record")
			}
		})
	}
	// The domain supports both currencies without converting between them.
	usd := state(t, ledger.Credit, 0, 1, 1)
	usd.Money = amount(t, 1, money.USD)
	usd.BalanceBefore = amount(t, 0, money.USD)
	usd.BalanceAfter = amount(t, 1, money.USD)
	if _, err := ledger.New(usd); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidEntries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ledger.State)
		want   error
	}{
		{"empty entry ID", func(s *ledger.State) { s.ID = "" }, ledger.ErrInvalidIdentity},
		{"invalid wallet ID", func(s *ledger.State) { s.WalletID = "wallet" }, ledger.ErrInvalidIdentity},
		{"nil transaction ID", func(s *ledger.State) { s.TransactionID = "00000000-0000-0000-0000-000000000000" }, ledger.ErrInvalidIdentity},
		{"uppercase ID", func(s *ledger.State) { s.ID = "0192F291-27DD-7D3F-8071-5F8685DEEF37" }, ledger.ErrInvalidIdentity},
		{"malformed hex", func(s *ledger.State) { s.ID = "z192f291-27dd-7d3f-8071-5f8685deef37" }, ledger.ErrInvalidIdentity},
		{"missing direction", func(s *ledger.State) { s.Direction = "" }, ledger.ErrInvalidDirection},
		{"unknown direction", func(s *ledger.State) { s.Direction = "TRANSFER" }, ledger.ErrInvalidDirection},
		{"lowercase direction", func(s *ledger.State) { s.Direction = "credit" }, ledger.ErrInvalidDirection},
		{"zero movement", func(s *ledger.State) { s.Money = amount(t, 0, money.BRL) }, ledger.ErrNonPositiveAmount},
		{"negative movement", func(s *ledger.State) { s.Money = amount(t, -1, money.BRL) }, ledger.ErrNonPositiveAmount},
		{"uninitialized movement", func(s *ledger.State) { s.Money = money.Money{} }, money.ErrInvalidMoney},
		{"uninitialized before", func(s *ledger.State) { s.BalanceBefore = money.Money{} }, money.ErrInvalidMoney},
		{"uninitialized after", func(s *ledger.State) { s.BalanceAfter = money.Money{} }, money.ErrInvalidMoney},
		{"negative before", func(s *ledger.State) { s.BalanceBefore = amount(t, -1, money.BRL) }, ledger.ErrNegativeBalance},
		{"negative after", func(s *ledger.State) { s.BalanceAfter = amount(t, -1, money.BRL) }, ledger.ErrNegativeBalance},
		{"movement currency", func(s *ledger.State) { s.Money = amount(t, 100, money.USD) }, money.ErrCurrencyMismatch},
		{"before currency", func(s *ledger.State) { s.BalanceBefore = amount(t, 1000, money.USD) }, money.ErrCurrencyMismatch},
		{"after currency", func(s *ledger.State) { s.BalanceAfter = amount(t, 1100, money.USD) }, money.ErrCurrencyMismatch},
		{"missing time", func(s *ledger.State) { s.CreatedAt = time.Time{} }, ledger.ErrInvalidTime},
		{"wrong resulting balance", func(s *ledger.State) { s.BalanceAfter = amount(t, 1101, money.BRL) }, ledger.ErrBalanceMismatch},
		{"wrong initial balance", func(s *ledger.State) { s.BalanceBefore = amount(t, 999, money.BRL) }, ledger.ErrBalanceMismatch},
		{"wrong direction", func(s *ledger.State) { s.Direction = ledger.Debit }, ledger.ErrBalanceMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := state(t, ledger.Credit, 1000, 100, 1100)
			tc.mutate(&input)
			for _, constructor := range []func(ledger.State) (ledger.Entry, error){ledger.New, ledger.Rehydrate} {
				if _, err := constructor(input); !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
			}
		})
	}
}

func TestOverflowAndInsufficientBalance(t *testing.T) {
	for _, tc := range []struct {
		input ledger.State
		want  error
	}{
		{state(t, ledger.Credit, math.MaxInt64, 1, math.MaxInt64), money.ErrOverflow},
		{state(t, ledger.Credit, 1, math.MaxInt64, 0), money.ErrOverflow},
		{state(t, ledger.Debit, 100, 101, 0), ledger.ErrNegativeBalance},
		{state(t, ledger.Debit, 0, math.MaxInt64, 0), ledger.ErrNegativeBalance},
	} {
		for _, constructor := range []func(ledger.State) (ledger.Entry, error){ledger.New, ledger.Rehydrate} {
			if _, err := constructor(tc.input); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		}
	}
}

func TestSnapshotsAreDetached(t *testing.T) {
	input := state(t, ledger.Credit, 0, 10000, 10000)
	entry, err := ledger.New(input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := entry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	input.Direction = ledger.Debit
	input.Money = amount(t, 1, money.USD)
	copy, err := entry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	copy.ID = transactionID
	copy.BalanceAfter = amount(t, 500, money.BRL)
	unchanged, err := entry.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if unchanged != original {
		t.Fatal("entry was changed through a detached snapshot")
	}
	restored, err := ledger.Rehydrate(original)
	if err != nil {
		t.Fatal(err)
	}
	original.BalanceBefore = amount(t, 999, money.BRL)
	restoredState, err := restored.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if restoredState != unchanged {
		t.Fatal("rehydrated entry retained mutable state")
	}
}

func TestZeroValueIsInvalid(t *testing.T) {
	var entry ledger.Entry
	if !errors.Is(entry.Validate(), ledger.ErrInvalidIdentity) {
		t.Fatal("zero entry accepted")
	}
	if _, err := entry.Snapshot(); !errors.Is(err, ledger.ErrInvalidIdentity) {
		t.Fatal(err)
	}
}
