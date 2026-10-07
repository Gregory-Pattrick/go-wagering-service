# Immutable Wallet Ledger

`internal/domain/ledger` models one financial movement of one wallet.
An entry records its ID, wallet ID, transaction ID, direction, positive Money
amount, balance before, balance after and creation time.

## Invariants

- IDs must be nonzero canonical lowercase UUIDs.
- Direction must be DEBIT or CREDIT.
- The movement amount must be strictly positive.
- Both balances must be nonnegative.
- Amount and balances must use the same supported currency.
- CREDIT requires `balanceAfter = balanceBefore + money`.
- DEBIT requires `balanceAfter = balanceBefore - money`.
- Overflow and missing creation timestamps are rejected.

All arithmetic uses the Money value object and exact integer minor units.
Creation timestamps are normalized to UTC. The Go zero value is invalid.
Errors support `errors.Is`; money validation and overflow errors are preserved.

`New` and `Rehydrate` enforce the same invariants. Rehydration does not change
balances, execute a transaction or emit events. Entry state is private, and
snapshots are detached values. There are no mutation methods. A correction
requires a new compensating transaction and entry, preserving the original.

## Financial Examples

| Movement | Before | Amount | After | Direction |
| --- | --- | --- | --- | --- |
| Positive opening | 0.00 BRL | 100.00 BRL | 100.00 BRL | CREDIT |
| BET | 100.00 BRL | 80.00 BRL | 20.00 BRL | DEBIT |
| WIN | 20.00 BRL | 30.00 BRL | 50.00 BRL | CREDIT |
| REFUND of BET | 20.00 BRL | 80.00 BRL | 100.00 BRL | CREDIT |

ROLLBACK uses the opposite direction of its eligible original movement.
LOSS and rejected operations must not create entries. Zero-balance wallet
creation also creates no financial ledger entry.

The entry constructor validates monetary consistency. It does not query the
transaction or establish whether an operation is eligible. The processing
service must validate transaction kind, status, reference and ownership before
constructing the appropriate entry. A mathematically valid entry alone does
not prove that a business operation is authorized or correct.

## Persistence and Double Entry

The database migrations enforce these additional guarantees:

- UNIQUE(wallet_id, transaction_id) prevents duplicate wallet movements.
- Foreign keys and cross-record checks enforce identity and financial consistency.
- Database permissions and triggers prevent UPDATE, DELETE and TRUNCATE.
- Wallet balance, ledger, transaction state and outbox must share one SQL commit;
  inbox completion joins that commit when processing a message.

The optional double-entry journal is implemented separately from this wallet
ledger. It records balanced postings without adding a second wallet ledger row
for the same movement; see accounting.md.

## Verification

Domain tests cover valid credits and debits, exact depletion, both supported
currencies, invalid identities and values, wrong balance equations, numeric
limits, insufficient balance, detached snapshots and validated rehydration.

```powershell
go test -count=1 ./internal/domain/ledger
```

These tests verify the domain object. They do not prove database immutability,
atomic commits, duplicate protection or multi-process financial correctness.
Those guarantees are exercised by the PostgreSQL and distributed suites.
