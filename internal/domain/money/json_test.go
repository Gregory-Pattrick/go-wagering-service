package money

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestJSON(t *testing.T) {
	for _, input := range []string{`{"amount":"25.00","currency":"BRL"}`, ` {"currency":"BRL", "amount":"25.00"} `} {
		m, err := ParseJSON([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(m)
		if err != nil || string(data) != `{"amount":"25.00","currency":"BRL"}` {
			t.Fatalf("JSON: %s, %v", data, err)
		}
	}
	for _, input := range []string{
		``, `null`, `[]`, `true`, `{}`, `{"amount":"1.00"}`, `{"currency":"BRL"}`,
		`{"amount":1.00,"currency":"BRL"}`, `{"amount":9007199254740993,"currency":"BRL"}`,
		`{"amount":null,"currency":"BRL"}`, `{"amount":{},"currency":"BRL"}`,
		`{"amount":[],"currency":"BRL"}`, `{"amount":true,"currency":"BRL"}`,
		`{"amount":"1.00","currency":1}`, `{"Amount":"1.00","currency":"BRL"}`,
		`{"amount":"1.00","currency":"BRL","extra":1}`,
		`{"amount":"1.00","amount":"2.00","currency":"BRL"}`,
		`{"amount":"1.00","currency":"BRL","currency":"USD"}`,
		`{"amount":"1.00","currency":"BRL"} {}`,
		`{"amount":"1.00","currency":"BRL"} garbage`,
		`{"amount":"1.00","currency":"BRL",}`, `{"amount":"1.00","currency":"BRL"`,
	} {
		if _, err := ParseJSON([]byte(input)); !errors.Is(err, ErrInvalidJSON) {
			t.Errorf("ParseJSON(%s): %v", input, err)
		}
	}
	for _, tc := range []struct {
		input string
		want  error
	}{
		{`{"amount":"-1.00","currency":"BRL"}`, ErrInvalidAmount},
		{`{"amount":"1.0","currency":"BRL"}`, ErrInvalidAmount},
		{`{"amount":"1.00","currency":"EUR"}`, ErrInvalidCurrency},
		{`{"amount":"92233720368547758.08","currency":"BRL"}`, ErrOverflow},
	} {
		if _, err := ParseJSON([]byte(tc.input)); !errors.Is(err, tc.want) {
			t.Errorf("ParseJSON(%s): %v", tc.input, err)
		}
	}
	negative := mustMinor(t, -1, BRL)
	data, err := json.Marshal(negative)
	if err != nil || string(data) != `{"amount":"-0.01","currency":"BRL"}` {
		t.Fatalf("signed output: %s, %v", data, err)
	}
}
