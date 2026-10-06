# External Financial Processing Rules

`internal/domain/processing.Evaluate` accepts validated transaction and wallet
snapshots plus an optional reference. It returns a decision containing the next
transaction, wallet and optional immutable ledger entry. It performs no I/O and
does not modify its inputs. A returned decision is not a committed operation.

## Effects

| Kind | Effect | Reference |
| --- | --- | --- |
| BET | Debit a positive amount | None |
| WIN | Credit a positive amount | Optional processed BET |
| LOSS | Keep balance and version; no entry | None |
| REFUND | Credit the original BET amount | Required processed BET |
| ROLLBACK | Invert the original ledger direction and amount | Required processed BET, WIN or REFUND |

OPENING is rejected by this evaluator and belongs to the internal wallet-opening
flow. Terminal transactions must be replayed from persistence instead of being
submitted to Evaluate again.

The wallet ID, player and currency must match the operation. A wallet mismatch
is rejected without a financial result snapshot so another wallet's balance is
not disclosed. Authentication and provider authorization remain responsibilities
of the application and transport boundaries.

Each successful movement produces a single ledger entry, increments the wallet
version once and records the resulting balance/version in the transaction.
LOSS produces a processed transaction with the unchanged balance/version and no
ledger entry. Rejections preserve the original wallet and create no entry.

## Reference Resolution

The repository must look up references by `(provider_id,
external_transaction_id)` in the same SQL transaction as the locked wallet.
The evaluator also checks that identity, wallet, player, currency and round match.
It requires exact amount equality for REFUND and ROLLBACK, but a WIN amount may
differ from its referenced BET. Game IDs remain required but need not match;
reference matching follows the fields explicitly required by the challenge.

Missing references and eligible references still in PENDING or PENDING_REFERENCE
produce PENDING_REFERENCE without changing the wallet. Evaluating an already
waiting operation does not recreate the state transition. Scheduling, backoff,
TTL, durable retries and eventual REFERENCE_NOT_FOUND rejection are pending
application-worker work, not part of this in-memory evaluator.

References ending in REJECTED or FAILED produce REFERENCE_NOT_PROCESSED.
Ineligible kinds produce REFERENCE_KIND_INVALID. Context mismatches and partial
reversals produce REFERENCE_MISMATCH. A processed reference must have a valid
ledger matching its transaction, wallet, amount, expected direction and original
result balance. An inconsistent stored record returns a technical error rather
than silently constructing a financial decision.

## Compensation Policy

A BET may have one successful direct compensation: REFUND or ROLLBACK. Once one
has succeeded, both another REFUND and a ROLLBACK targeting that BET are rejected
with ALREADY_REVERSED. A WIN or REFUND may likewise be rolled back only once.

ROLLBACK of REFUND debits that refund's credit. It never reopens the original
BET's right to compensation. ROLLBACK of ROLLBACK, LOSS or OPENING is ineligible.
WIN is not a reversal and does not consume the compensation right.

The Reference.Compensated flag must be derived from committed compensation
history under database coordination. It is not client input and is not an
in-memory idempotency mechanism. The evaluator's tests exercise policy decisions
from that snapshot; database constraints and concurrent integration tests must
still prove that two processes cannot both claim the same compensation.

## Rejections and Technical Errors

| Condition | Outcome |
| --- | --- |
| BET cannot be covered | REJECTED / INSUFFICIENT_FUNDS |
| Reversal debit cannot be covered | REJECTED / REVERSAL_INSUFFICIENT_FUNDS |
| Credit would overflow int64 | REJECTED / BALANCE_OVERFLOW |
| Invalid wallet context | REJECTED / WALLET_MISMATCH, without result |
| Reference already compensated | REJECTED / ALREADY_REVERSED |
| Invalid or inconsistent domain record | Error; no decision to persist |
| Wallet version overflow | Error; no decision to persist |

A rejection involving the correctly matched wallet keeps the balance/version
observed at the decision. Later wallet changes never change that transaction's
result snapshot. Missing-wallet lookup is handled by the application, which can
record WALLET_NOT_FOUND without fabricating a wallet or financial result.

The occurrence time is the latest of the supplied time, wallet update time and
transaction update time. The same value is used for the successful transaction,
wallet and ledger entry. Ordering still relies on version and database rules.

## Application Contract and Remaining Work

The future application service must:

1. Authenticate/authorize, resolve persistent idempotency and lock the wallet.
2. Load the reference, original ledger and compensation history consistently.
3. Evaluate the financial rules using those snapshots.
4. Atomically persist the transaction, changed wallet, optional ledger,
   compensation claim, inbox completion when applicable and outbox records.
5. Commit before making events available to a publisher.

A successful LOSS requires WagerTransactionProcessed, without
WalletBalanceChanged. A successful movement requires both financial events.
A business rejection requires its rejection event. Event construction and outbox
persistence are not implemented in this step.

No local mutex, in-memory flag or passing unit test establishes cross-process
correctness. The PostgreSQL locking, uniqueness constraints, durable scheduling
and fault-injection tests remain necessary.

## Verification

Tests cover all five effects, reversal directions, missing/pending/unsuccessful
references, context and amount mismatches, compensation combinations, rollback
of a refund, invalid stored ledger data, failure-code distinctions, exact balances,
versions, unchanged inputs and original result snapshots after later movements.

```powershell
go test -count=1 ./internal/domain/processing
```
