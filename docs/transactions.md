# Transaction Identity and Lifecycle

`internal/domain/transaction` implements transaction construction, validated
rehydration, canonical business payload hashing and lifecycle transitions.
It has no database, HTTP, SQS or dependency-injection dependencies.

## Origins and Amounts

External transactions carry internal and external IDs, provider, idempotency
key, wallet, player, round, game, kind, money and an optional external reference.
Internal, wallet and player IDs are canonical lowercase nonzero UUIDs. Other
identifiers must be nonempty valid UTF-8 strings without surrounding whitespace
or ASCII control characters. Transport adapters must normalize UUID input
before domain construction and hashing.

| Kind | Amount | External reference |
| --- | --- | --- |
| BET | Positive | Rejected |
| WIN | Positive | Optional |
| LOSS | Exactly zero | Rejected |
| REFUND | Positive | Required |
| ROLLBACK | Positive | Required |

Only WIN, REFUND and ROLLBACK accept references in this contract. A transaction
cannot reference its own external ID. Actual reference eligibility and financial
effects are responsibilities of the domain processing and application packages.

`NewExternal` rejects OPENING. `NewOpening` accepts only a positive amount and
internal transaction, wallet and player identities. It has no external ID,
provider, idempotency key, business hash, round, game or reference. Zero-balance
wallet creation must not create an OPENING transaction. A processed opening's
result must equal its amount and have wallet version 1.

The caller must supply a stable opening identity. Database constraints and the
wallet-opening SQL transaction prevents duplicate opening credits.

## State Machine

| Current state | Allowed next states |
| --- | --- |
| PENDING | PENDING_REFERENCE, PROCESSED, REJECTED, FAILED |
| PENDING_REFERENCE | PROCESSED, REJECTED, FAILED |
| PROCESSED | None |
| REJECTED | None |
| FAILED | None |

PENDING_REFERENCE requires an external reference. A retry keeps that state
while updating a separate durable scheduling record; it does not reset the
transaction or repeatedly call WaitForReference.

Processed transactions require a valid, nonnegative balance snapshot and
positive wallet version. When a reference was supplied, a resolved internal
reference ID is required. Rejected transactions require a supported business
failure code and may carry an observed result. If no valid wallet was observed,
no balance is fabricated. FAILED records use PERMANENT_INFRASTRUCTURE_FAILURE
and do not claim a financial result.

Terminal transactions cannot be transitioned again, even to the same state.
Replay reads persisted state. It does not re-run MarkProcessed or financial
operations. Creation timestamps remain stable; update timestamps never regress.
Rehydration validates state but emits no events and applies no balance change.
Snapshots and result objects are detached from the aggregate.

MarkProcessed checks lifecycle and result structure. It does not prove that
a reference is eligible or that a ledger entry exists. The financial service
must make that decision and atomically persist all associated effects.

## Failure Codes

| Code | Intended business meaning |
| --- | --- |
| INSUFFICIENT_FUNDS | BET cannot be covered |
| REVERSAL_INSUFFICIENT_FUNDS | A reversal debit cannot be covered |
| REFERENCE_NOT_FOUND | Reference wait policy exhausted |
| REFERENCE_NOT_PROCESSED | Referenced operation ended unsuccessfully |
| REFERENCE_MISMATCH | Reference ownership, context or reversal amount differs |
| REFERENCE_KIND_INVALID | Referenced kind is not eligible |
| ALREADY_REVERSED | A successful compensation already exists |
| WALLET_NOT_FOUND | Requested wallet does not exist |
| WALLET_MISMATCH | Wallet ownership or currency does not match |
| BALANCE_OVERFLOW | Resulting balance exceeds the supported range |
| PERMANENT_INFRASTRUCTURE_FAILURE | Explicitly classified, audited permanent failure |

Failure code selection for each financial rule will be implemented in the
processing service. Constructors reject malformed input before acceptance;
those errors are not automatically persisted as terminal business rejections.

Retryable database or broker outages must not call FailPermanent. Unknown
commit outcomes require a retry with the same identity and a database lookup.
A permanent failure is recorded only when that classification is justified and
no successful outcome has already been committed.

## Canonical Hash and Future Idempotency

SHA-256 covers these keys in lexical order:

- externalTransactionId
- gameId
- kind
- money (amount, currency)
- playerId
- providerId
- referenceExternalTransactionId
- roundId
- walletId

Money uses its canonical decimal string. An absent reference is encoded as an
empty string. JSON is produced with Go encoding/json's default escaping and no
extra whitespace. No floating-point monetary conversion occurs.

Internal transaction ID, idempotency key, creation time and transport metadata
are excluded. HTTP and SQS must construct the same ExternalInput. Rehydration
recomputes the hash and rejects inconsistent stored input. This hash is a
business identity aid, not a signature or proof against database tampering.

Hash construction alone is not durable idempotency. Database constraints
enforce (provider_id, idempotency_key) and (provider_id,
external_transaction_id). The application policy rejects a different key for an
existing external ID, even when the business hash matches. Authorization must
precede replay lookup or disclosure of a stored result.

## Application integration

The processing domain evaluates financial effects and reference eligibility.
The application commits state, balance, ledger, journal, inbox where applicable,
and outbox together. Durable reference work permits another worker to resume
unresolved references. Database constraints enforce duplicate protection.

Tests cover amount policies, identities, internal opening, the transition matrix,
terminal protection, result snapshots, detached copies, invalid persisted states
and canonical hashing. These are domain tests, not distributed-system proofs.
