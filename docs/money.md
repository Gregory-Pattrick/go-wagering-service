# Money Value Object

`internal/domain/money` represents money as signed `int64` minor units and
an explicit currency. BRL and USD are supported, both with two decimal places.
No parsing, arithmetic or JSON operation uses floating-point amounts.
The package has no infrastructure dependencies.

## External Input

Use `money.Parse(amount, currency)` or `money.ParseJSON(data)`.
Amounts must be nonnegative strings with exactly two fractional digits:
`"0.00"`, `"0.01"`, `"25.00"`.
Signs, whitespace inside the amount, exponents, redundant leading zeros,
commas and missing or additional fractional digits are rejected.
The largest accepted amount is `"92233720368547758.07"`.
Whether a business operation allows zero is enforced by that operation.

The JSON constructor accepts exactly `amount` and `currency`, both strings.
Duplicate, unknown, missing and incorrectly cased keys are rejected, as are
JSON numbers and trailing JSON values. Field order and JSON whitespace do
not affect the result.

```json
{"amount":"25.00","currency":"BRL"}
```

Use a transport DTO with `json.RawMessage` and call `ParseJSON` for input.
Do not decode directly into `Money` using `json.Unmarshal`: its fields are
private, and it intentionally has no mutating JSON decoder. Validate domain
values at business boundaries so an uninitialized value cannot be used.

## Internal Values and Arithmetic

`FromMinor` constructs values from exact integer minor units, including
negative values needed for ledger deltas. It is not an external input parser.
The zero Go value is invalid; use `Zero(currency)` for a valid monetary zero.

`Add`, `Subtract` and `Compare` require initialized values with the same
currency. Arithmetic checks overflow before computing its result.
`Negate` rejects `math.MinInt64`. Subtracting `math.MinInt64` from itself
is valid and produces zero. Operands remain unchanged.

`Amount` formats all signed values exactly, including the internal minimum
`-92233720368547758.08`. JSON output uses string amounts and can represent
signed internal deltas. External parsing deliberately rejects those negative
values; it is not a persistence decoder for signed ledger entries.

Errors are exported sentinels compatible with `errors.Is`. Currency conversion,
rounding, wallet balance constraints, persistence and financial operations
are outside this package.

## Verification

Tests cover canonical parsing, supported currencies, invalid zero values,
JSON validation, signed formatting, immutability and exact arithmetic.
Boundary combinations and deterministic random inputs are compared with an
independent `math/big` arithmetic oracle, including overflow outcomes.

```powershell
go test -count=1 ./internal/domain/money
```
