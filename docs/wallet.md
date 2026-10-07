# Wallet Aggregate

`internal/domain/wallet` owns wallet identity, player identity, balance and
currency, version, creation time and update time. Currency is carried by
`Money`; there is no second mutable currency field.

## Creation and Restoration

`New` accepts a valid zero or positive balance and starts at version `1`.
Wallet and player IDs must be nonzero lowercase UUIDs in canonical hyphenated
form. ID generation belongs to the application layer; no UUID version is
required by this aggregate. The transport must normalize accepted UUID input
before constructing domain values and hashing business payloads.

`Rehydrate` restores a validated persistence snapshot without creating any
movement or event. Versions must be positive, timestamps must be nonzero,
and the update time cannot precede creation. Timestamps are normalized to UTC.
The zero Go value of `Wallet` is invalid.

## Balance Changes

`Credit` and `Debit` require strictly positive amounts in the wallet currency.
A debit cannot exceed the balance. Credits reject monetary overflow, and
both operations reject version overflow. Successful movements increment the
version exactly once. They return a new wallet; callers must use that result.
The original aggregate and detached snapshots remain unchanged.

Business operations with no movement, such as `LOSS`, must not call these
methods. They retain the wallet balance and version. Initial zero balances
are allowed even though zero-valued credit and debit calls are rejected.

Creation time remains unchanged. Update time is the later of the supplied
time and the previous update time, preventing timestamp regression when
application clocks differ. Financial ordering relies on the wallet version,
not timestamp uniqueness.

## Persistence Boundary and Remaining Work

This package does not persist wallets, create ledger entries or publish events.
Its tests prove in-memory domain invariants, not cross-process consistency.

The application unit of work locks the wallet row with
`SELECT ... FOR UPDATE`, rehydrate it, apply the operation and persist its
balance, ledger, transaction state and outbox records in one SQL transaction.
All writers must follow that protocol. A unique database constraint must
protect `(player_id, currency)`; an in-memory aggregate cannot enforce global
uniqueness. No process-local mutex will be used as a distributed lock.

Positive opening balances require an internal `OPENING` transaction, credit
ledger entry and both financial events in the same commit as wallet creation,
with wallet version `1`. Zero opening balances require none of these financial
records. These behaviors are implemented by the application and PostgreSQL adapter;
the financial integration suite verifies them against real PostgreSQL.

## Verification

Tests cover creation, rehydration, detached snapshots, invalid states, exact
balance changes, insufficient funds, currency mismatch, zero and negative
movements, monetary and version overflow, and timestamp behavior.

```powershell
go test -count=1 ./internal/domain/wallet
```
