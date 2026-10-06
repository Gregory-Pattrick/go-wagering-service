package money

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func mustMinor(t *testing.T, n int64, c Currency) Money {
	t.Helper()
	m, err := FromMinor(n, c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
	}{
		{"0.00", 0}, {"0.01", 1}, {"0.10", 10}, {"25.00", 2500},
		{"92233720368547758.07", math.MaxInt64},
	} {
		for _, currency := range []Currency{BRL, USD} {
			m, err := Parse(tc.input, currency)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			units, _ := m.MinorUnits()
			amount, _ := m.Amount()
			code, _ := m.Currency()
			if units != tc.want || amount != tc.input || code != currency {
				t.Fatalf("incorrect conversion: %+v", m)
			}
		}
	}
	for _, input := range []string{"", "0", "1", "1.0", "1.000", ".00", "00.00", "01.00", "-1.00", "-0.00", "+1.00", " 1.00", "1.00 ", "1,00", "1e2", "NaN", "Infinity", "１.00", "1.0x", "1..00"} {
		if _, err := Parse(input, BRL); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("Parse(%q): %v", input, err)
		}
	}
	for _, input := range []string{"92233720368547758.08", "999999999999999999999999.00"} {
		if _, err := Parse(input, BRL); !errors.Is(err, ErrOverflow) {
			t.Errorf("Parse(%q): %v", input, err)
		}
	}
	for _, c := range []Currency{"", "brl", "EUR", " BRL"} {
		if _, err := Parse("1.00", c); !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("currency %q: %v", c, err)
		}
		if _, err := FromMinor(1, c); !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("FromMinor currency %q: %v", c, err)
		}
	}
}

func TestFormattingAndNegation(t *testing.T) {
	for _, tc := range []struct {
		n      int64
		amount string
		sign   int
	}{
		{0, "0.00", 0}, {1, "0.01", 1}, {-1, "-0.01", -1}, {-100, "-1.00", -1},
		{math.MinInt64, "-92233720368547758.08", -1}, {math.MaxInt64, "92233720368547758.07", 1},
	} {
		m := mustMinor(t, tc.n, BRL)
		amount, err := m.Amount()
		if err != nil || amount != tc.amount {
			t.Fatalf("Amount(%d) = %q, %v", tc.n, amount, err)
		}
		sign, _ := m.Sign()
		if sign != tc.sign {
			t.Fatalf("Sign(%d) = %d", tc.n, sign)
		}
		neg, err := m.Negate()
		if tc.n == math.MinInt64 {
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("Negate minimum: %v", err)
			}
		} else if err != nil || neg.minor != -tc.n {
			t.Fatalf("Negate(%d): %+v, %v", tc.n, neg, err)
		}
	}
	zero, err := Zero(USD)
	if err != nil || zero.minor != 0 || zero.currency != USD {
		t.Fatal("invalid zero")
	}
}

// An independent arbitrary-precision oracle checks both values and overflow.
func TestArithmeticAgainstBigIntegers(t *testing.T) {
	edges := []int64{math.MinInt64, math.MinInt64 + 1, -100, -1, 0, 1, 100, math.MaxInt64 - 1, math.MaxInt64}
	pairs := make([][2]int64, 0)
	for _, a := range edges {
		for _, b := range edges {
			pairs = append(pairs, [2]int64{a, b})
		}
	}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		pairs = append(pairs, [2]int64{int64(rng.Uint64()), int64(rng.Uint64())})
	}
	for _, pair := range pairs {
		a, b := mustMinor(t, pair[0], BRL), mustMinor(t, pair[1], BRL)
		originalA, originalB := a, b
		for _, subtract := range []bool{false, true} {
			expected := new(big.Int)
			var result Money
			var err error
			if subtract {
				expected.Sub(big.NewInt(pair[0]), big.NewInt(pair[1]))
				result, err = a.Subtract(b)
			} else {
				expected.Add(big.NewInt(pair[0]), big.NewInt(pair[1]))
				result, err = a.Add(b)
			}
			if !expected.IsInt64() {
				if !errors.Is(err, ErrOverflow) {
					t.Fatalf("pair %v subtract=%v: expected overflow, got %v", pair, subtract, err)
				}
			} else if err != nil || result.minor != expected.Int64() || result.currency != BRL {
				t.Fatalf("pair %v subtract=%v: got %+v, %v; want %s", pair, subtract, result, err, expected)
			}
		}
		cmp, err := a.Compare(b)
		if err != nil || cmp != big.NewInt(pair[0]).Cmp(big.NewInt(pair[1])) {
			t.Fatalf("comparison: %v", pair)
		}
		if a != originalA || b != originalB {
			t.Fatal("operands were mutated")
		}
	}
	a, _ := Parse("0.10", BRL)
	b, _ := Parse("0.20", BRL)
	sum, err := a.Add(b)
	amount, _ := sum.Amount()
	if err != nil || amount != "0.30" {
		t.Fatalf("exact addition: %s, %v", amount, err)
	}
}

func TestInvalidValuesAndCurrencyMismatch(t *testing.T) {
	var invalid Money
	valid := mustMinor(t, 1, BRL)
	usd := mustMinor(t, 1, USD)
	checks := []func() error{
		invalid.Validate,
		func() error { _, e := invalid.Amount(); return e },
		func() error { _, e := invalid.MinorUnits(); return e },
		func() error { _, e := invalid.Currency(); return e },
		func() error { _, e := invalid.Sign(); return e },
		func() error { _, e := invalid.Negate(); return e },
		func() error { _, e := json.Marshal(invalid); return e },
	}
	for _, check := range checks {
		if err := check(); !errors.Is(err, ErrInvalidMoney) {
			t.Errorf("invalid value accepted: %v", err)
		}
	}
	for _, pair := range [][2]Money{{invalid, valid}, {valid, invalid}, {valid, usd}} {
		want := ErrInvalidMoney
		if pair[1] == usd {
			want = ErrCurrencyMismatch
		}
		_, addErr := pair[0].Add(pair[1])
		_, subErr := pair[0].Subtract(pair[1])
		_, cmpErr := pair[0].Compare(pair[1])
		for _, err := range []error{addErr, subErr, cmpErr} {
			if !errors.Is(err, want) {
				t.Errorf("got %v, want %v", err, want)
			}
		}
	}
}
