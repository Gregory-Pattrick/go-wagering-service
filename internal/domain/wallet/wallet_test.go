package wallet_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/money"
	"github.com/Gregory-Pattrick/go-wagering-service/internal/domain/wallet"
)

const walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
const playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"

var created = time.Date(2026, 10, 5, 18, 0, 0, 0, time.FixedZone("local", -3*3600))

func amount(t *testing.T, cents int64, currency money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(cents, currency)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func newWallet(t *testing.T, cents int64) wallet.Wallet {
	t.Helper()
	w, err := wallet.New(walletID, playerID, amount(t, cents, money.BRL), created)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func snapshot(t *testing.T, w wallet.Wallet) wallet.State {
	t.Helper()
	s, err := w.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func assertBalance(t *testing.T, w wallet.Wallet, cents, version int64) {
	t.Helper()
	s := snapshot(t, w)
	n, err := s.Balance.MinorUnits()
	if err != nil || n != cents || s.Version != version {
		t.Fatalf("balance=%d version=%d error=%v", n, s.Version, err)
	}
}

func TestCreationAndRehydration(t *testing.T) {
	for _, cents := range []int64{0, 10000, math.MaxInt64} {
		w := newWallet(t, cents)
		assertBalance(t, w, cents, 1)
		s := snapshot(t, w)
		if s.ID != walletID || s.PlayerID != playerID || !s.CreatedAt.Equal(created) || !s.UpdatedAt.Equal(created) || s.CreatedAt.Location() != time.UTC {
			t.Fatal("incorrect creation snapshot")
		}
		s.Version = 7
		s.UpdatedAt = created.Add(time.Hour)
		restored, err := wallet.Rehydrate(s)
		if err != nil {
			t.Fatal(err)
		}
		assertBalance(t, restored, cents, 7)
		s.Version = 99
		s.Balance = amount(t, 50, money.USD)
		assertBalance(t, restored, cents, 7)
	}
}

func TestInvalidSnapshots(t *testing.T) {
	base := snapshot(t, newWallet(t, 10000))
	for _, tc := range []struct {
		name   string
		change func(*wallet.State)
		want   error
	}{
		{"empty ID", func(s *wallet.State) { s.ID = "" }, wallet.ErrInvalidID},
		{"malformed ID", func(s *wallet.State) { s.ID = "not-a-uuid" }, wallet.ErrInvalidID},
		{"uppercase ID", func(s *wallet.State) { s.ID = "0192F291-27DD-7D3F-8071-5F8685DEEF37" }, wallet.ErrInvalidID},
		{"nil ID", func(s *wallet.State) { s.ID = "00000000-0000-0000-0000-000000000000" }, wallet.ErrInvalidID},
		{"empty player", func(s *wallet.State) { s.PlayerID = "" }, wallet.ErrInvalidID},
		{"invalid money", func(s *wallet.State) { s.Balance = money.Money{} }, money.ErrInvalidMoney},
		{"negative balance", func(s *wallet.State) { s.Balance = amount(t, -1, money.BRL) }, wallet.ErrInvalidBalance},
		{"zero version", func(s *wallet.State) { s.Version = 0 }, wallet.ErrInvalidVersion},
		{"negative version", func(s *wallet.State) { s.Version = -1 }, wallet.ErrInvalidVersion},
		{"missing creation", func(s *wallet.State) { s.CreatedAt = time.Time{} }, wallet.ErrInvalidTime},
		{"missing update", func(s *wallet.State) { s.UpdatedAt = time.Time{} }, wallet.ErrInvalidTime},
		{"backwards snapshot", func(s *wallet.State) { s.UpdatedAt = created.Add(-time.Second) }, wallet.ErrInvalidTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.change(&s)
			if _, err := wallet.Rehydrate(s); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := wallet.New(walletID, playerID, amount(t, -1, money.BRL), created); !errors.Is(err, wallet.ErrInvalidBalance) {
		t.Fatal(err)
	}
}

func TestMovements(t *testing.T) {
	initial := newWallet(t, 10000)
	debited, err := initial.Debit(amount(t, 8000, money.BRL), created.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertBalance(t, debited, 2000, 2)
	assertBalance(t, initial, 10000, 1)
	if _, err := debited.Debit(amount(t, 8000, money.BRL), created.Add(2*time.Second)); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatal(err)
	}
	assertBalance(t, debited, 2000, 2)
	empty, err := debited.Debit(amount(t, 2000, money.BRL), created.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertBalance(t, empty, 0, 3)
	credited, err := empty.Credit(amount(t, 1, money.BRL), created.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assertBalance(t, credited, 1, 4)
	s := snapshot(t, credited)
	if !s.CreatedAt.Equal(created) || !s.UpdatedAt.Equal(created.Add(3*time.Second)) {
		t.Fatal("incorrect movement timestamps")
	}
	// This is a sequential domain test, not a distributed concurrency proof.
}

func TestRejectedMovementsLeaveOriginalUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value money.Money
		now   time.Time
		want  error
	}{
		{"zero", amount(t, 0, money.BRL), created, wallet.ErrNonPositiveAmount},
		{"negative", amount(t, -1, money.BRL), created, wallet.ErrNonPositiveAmount},
		{"currency", amount(t, 1, money.USD), created, money.ErrCurrencyMismatch},
		{"uninitialized", money.Money{}, created, money.ErrInvalidMoney},
		{"time", amount(t, 1, money.BRL), time.Time{}, wallet.ErrInvalidTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWallet(t, 100)
			_, creditErr := w.Credit(tc.value, tc.now)
			_, debitErr := w.Debit(tc.value, tc.now)
			for _, err := range []error{creditErr, debitErr} {
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
			}
			assertBalance(t, w, 100, 1)
		})
	}
	full := newWallet(t, math.MaxInt64)
	if _, err := full.Credit(amount(t, 1, money.BRL), created); !errors.Is(err, money.ErrOverflow) {
		t.Fatal(err)
	}
	assertBalance(t, full, math.MaxInt64, 1)
	state := snapshot(t, newWallet(t, 100))
	state.Version = math.MaxInt64
	exhausted, err := wallet.Rehydrate(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, move := range []func(money.Money, time.Time) (wallet.Wallet, error){exhausted.Credit, exhausted.Debit} {
		if _, err := move(amount(t, 1, money.BRL), created); !errors.Is(err, wallet.ErrVersionOverflow) {
			t.Fatal(err)
		}
	}
	assertBalance(t, exhausted, 100, math.MaxInt64)
}

func TestClockRegressionAndZeroValue(t *testing.T) {
	w := newWallet(t, 100)
	next, err := w.Credit(amount(t, 1, money.BRL), created.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	assertBalance(t, next, 101, 2)
	if !snapshot(t, next).UpdatedAt.Equal(created) {
		t.Fatal("timestamp regressed")
	}
	var invalid wallet.Wallet
	if invalid.Validate() == nil {
		t.Fatal("zero wallet accepted")
	}
	if _, err := invalid.Snapshot(); err == nil {
		t.Fatal("zero snapshot accepted")
	}
	if _, err := invalid.Credit(amount(t, 1, money.BRL), created); err == nil {
		t.Fatal("zero credit accepted")
	}
	if _, err := invalid.Debit(amount(t, 1, money.BRL), created); err == nil {
		t.Fatal("zero debit accepted")
	}
}
