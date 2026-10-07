# Typed Financial Events

`internal/domain/events` constructs immutable event snapshots through
`ForTransition(before, after, ledgerEntry, metadata)`.
It does not publish messages or write outbox rows.

## Contracts

All envelopes contain `eventId`, `eventType`, `aggregateId`, `correlationId`,
optional `causationId`, `occurredAt`, `version` and typed `data`.
Constructors set the type and schema version (`1`). All events use wallet ID
as aggregate ID, supporting wallet-based SQS FIFO routing.

Occurrence time is the transaction decision's update time, serialized in UTC
RFC 3339 (with fractional seconds when present). Amounts are canonical decimal
strings inside Money objects. Correlation must be supplied by the application;
causation may identify the inbound message or another causal operation.
Event IDs are distinct, nonzero canonical UUIDs supplied by the application.

| Concrete event | Trigger | Data |
| --- | --- | --- |
| WagerTransactionProcessed | New successful decision, including LOSS and positive OPENING | Transaction identity, money and original balance/version result |
| WagerTransactionRejected | New business rejection, including expired reference wait | Transaction identity, failure code and observed result when available |
| WagerTransactionPendingReference | First transition to reference wait | Transaction identity and external reference |
| WalletBalanceChanged | Successful financial movement | walletId, transactionId, direction, money, balanceBefore, balanceAfter, walletVersion |

Transaction payloads contain `transactionId`, `walletId`, `playerId`, `origin`,
`kind` and `money`. External operations also carry a nested `external` object
with provider ID, external transaction ID, idempotency key, payload hash, round,
game and applicable reference IDs. Internal OPENING events use origin INTERNAL
and omit the entire external object; no invented provider metadata is needed.

## Transition and Ledger Rules

Successful movements produce two events in order: transaction processed, then
wallet balance changed. LOSS produces only transaction processed and must not
have a ledger entry. Rejections and pending references also forbid ledger entries.

The constructor checks that before and after describe the same operation and
that the transition is eligible. It cross-checks movement entries against the
transaction's identity, money, result balance, kind and occurrence time.
OPENING requires a credit from zero and the transaction's version-1 result.
ROLLBACK direction is determined by the processing rules using its original
reference ledger; event construction does not independently query that reference.

Repeated evaluation of the same pending-reference state produces no new logical
event. Terminal replay is rejected by this constructor: replay must read the
stored result and must not create fresh outbox records. FAILED does not produce
one of the four events because the challenge defines no failure event; that
outcome still requires persistent audit state and operational observability.

Zero-balance wallet creation has no OPENING transaction and does not call this
constructor. Therefore it produces none of these financial events.

## Snapshot and Delivery Guarantees

Events have private state, concrete typed payloads and no mutation methods.
JSON serialization returns fresh bytes; modifying returned bytes, caller metadata,
transaction snapshots or ledger snapshots cannot change an existing event.
Zero-valued event objects cannot be serialized as valid events.

The application must generate each event ID once, serialize the event and insert
those bytes into the outbox within the same SQL transaction as its financial
state. A publisher must send the stored snapshot and preserve eventId on retries.
It must not construct a replacement event or read the wallet's current balance.
Consumers must deduplicate by eventId because delivery will be at least once.

The constructor is not persistent deduplication: calling it repeatedly with the
same pre-decision data can recreate a batch. Database uniqueness and application
transaction boundaries must prevent duplicate logical outbox records.

The PostgreSQL adapter persists these snapshots and the publisher routes them
through SQS FIFO. Database, distributed and recovery suites exercise delivery.
Full event payloads are business data and should not be written to application logs.

## Verification

Tests cover concrete types, required envelope fields, UTC timestamps, monetary
string encoding, optional causation, positive internal opening, all external kinds,
LOSS without balance change, rejection without a fabricated result, pending retry
suppression, reference expiration, immutable snapshots and invalid transitions.

```powershell
go test -count=1 ./internal/domain/events
```
