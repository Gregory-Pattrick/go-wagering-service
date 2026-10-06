package money

import (
	"bytes"
	"encoding/json"
	"io"
)

func (m Money) MarshalJSON() ([]byte, error) {
	amount, err := m.Amount()
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Amount   string   `json:"amount"`
		Currency Currency `json:"currency"`
	}{Amount: amount, Currency: m.currency})
}

// ParseJSON constructs an external, nonnegative Money value. It rejects
// duplicate, unknown, missing or incorrectly cased fields and non-string
// values. The JSON decoder never converts monetary numbers to floats.
// Use this constructor, not json.Unmarshal into Money (whose fields are private).
func ParseJSON(data []byte) (Money, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return Money{}, ErrInvalidJSON
	}
	fields := make(map[string]string, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Money{}, ErrInvalidJSON
		}
		name, ok := token.(string)
		if !ok || (name != "amount" && name != "currency") {
			return Money{}, ErrInvalidJSON
		}
		if _, duplicate := fields[name]; duplicate {
			return Money{}, ErrInvalidJSON
		}
		value, err := decoder.Token()
		if err != nil {
			return Money{}, ErrInvalidJSON
		}
		text, ok := value.(string)
		if !ok {
			return Money{}, ErrInvalidJSON
		}
		fields[name] = text
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(fields) != 2 {
		return Money{}, ErrInvalidJSON
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Money{}, ErrInvalidJSON
	}
	return Parse(fields["amount"], Currency(fields["currency"]))
}
