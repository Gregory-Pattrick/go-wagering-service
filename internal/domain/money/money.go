// Package money implements exact two-decimal monetary values.
package money

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var (
	ErrInvalidAmount    = errors.New("invalid canonical monetary amount")
	ErrInvalidCurrency  = errors.New("unsupported currency")
	ErrInvalidMoney     = errors.New("uninitialized money value")
	ErrCurrencyMismatch = errors.New("monetary currencies do not match")
	ErrOverflow         = errors.New("monetary value exceeds int64 range")
	ErrInvalidJSON      = errors.New("invalid monetary JSON object")
)

// Currency is an explicitly supported ISO 4217 currency code.
type Currency string

const (
	BRL Currency = "BRL"
	USD Currency = "USD"
)

func (c Currency) Validate() error {
	if c != BRL && c != USD {
		return ErrInvalidCurrency
	}
	return nil
}

// Money is immutable through its public API. Its zero value is invalid.
// All operations return new values and leave their operands unchanged.
type Money struct {
	minor    int64
	currency Currency
}

// FromMinor constructs an internal value from exact minor units. Negative
// amounts are valid internally; external input must use Parse or ParseJSON.
func FromMinor(minor int64, currency Currency) (Money, error) {
	if err := currency.Validate(); err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: currency}, nil
}

func Zero(currency Currency) (Money, error) {
	return FromMinor(0, currency)
}

// Parse accepts only nonnegative canonical decimals with exactly two digits
// after the dot. Signs, whitespace, exponents and redundant leading zeros fail.
func Parse(amount string, currency Currency) (Money, error) {
	if err := currency.Validate(); err != nil {
		return Money{}, err
	}
	if len(amount) < 4 {
		return Money{}, ErrInvalidAmount
	}
	dot := len(amount) - 3
	if amount[dot] != '.' || (dot > 1 && amount[0] == '0') {
		return Money{}, ErrInvalidAmount
	}
	for index := 0; index < len(amount); index++ {
		if index == dot {
			continue
		}
		if amount[index] < '0' || amount[index] > '9' {
			return Money{}, ErrInvalidAmount
		}
	}
	// Remove the decimal separator; never parse a floating-point number.
	minor, err := strconv.ParseInt(amount[:dot]+amount[dot+1:], 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return Money{}, ErrOverflow
		}
		return Money{}, ErrInvalidAmount
	}
	return FromMinor(minor, currency)
}

func (m Money) Validate() error {
	if m.currency.Validate() != nil {
		return ErrInvalidMoney
	}
	return nil
}

func (m Money) MinorUnits() (int64, error) {
	if err := m.Validate(); err != nil {
		return 0, err
	}
	return m.minor, nil
}

func (m Money) Currency() (Currency, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	return m.currency, nil
}

func (m Money) Sign() (int, error) {
	if err := m.Validate(); err != nil {
		return 0, err
	}
	if m.minor < 0 {
		return -1, nil
	}
	if m.minor > 0 {
		return 1, nil
	}
	return 0, nil
}

func (m Money) compatible(other Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := other.Validate(); err != nil {
		return err
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, ErrOverflow
	}
	return FromMinor(m.minor+other.minor, m.currency)
}

func (m Money) Subtract(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	// Do not implement subtraction through negation: MinInt64 cannot be negated,
	// but operations such as MinInt64 - MinInt64 are valid and equal zero.
	if (other.minor > 0 && m.minor < math.MinInt64+other.minor) ||
		(other.minor < 0 && m.minor > math.MaxInt64+other.minor) {
		return Money{}, ErrOverflow
	}
	return FromMinor(m.minor-other.minor, m.currency)
}

func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return FromMinor(-m.minor, m.currency)
}

func (m Money) Compare(other Money) (int, error) {
	if err := m.compatible(other); err != nil {
		return 0, err
	}
	if m.minor < other.minor {
		return -1, nil
	}
	if m.minor > other.minor {
		return 1, nil
	}
	return 0, nil
}

// Amount returns a canonical decimal, including a minus sign for internal
// negative values. Formatting does not negate MinInt64 or round any value.
func (m Money) Amount() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	digits := strconv.FormatInt(m.minor, 10)
	sign := ""
	if digits[0] == '-' {
		sign, digits = "-", digits[1:]
	}
	if len(digits) < 3 {
		digits = strings.Repeat("0", 3-len(digits)) + digits
	}
	dot := len(digits) - 2
	return sign + digits[:dot] + "." + digits[dot:], nil
}
