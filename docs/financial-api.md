# Financial HTTP API

All business routes require a Keycloak access token. The service still relies on
an external IdP; it does not issue its own tokens. Provider identity is checked
against the verified principal before persistent identity lookup or replay.

## Routes

| Method and route | Actor | Behavior |
| --- | --- | --- |
| POST /wallets | Internal | Create a wallet; positive opening includes ledger, journal and events atomically |
| GET /wallets/{walletId} | Internal | Current balance and version |
| GET /wallets/{walletId}/ledger | Internal | Stable cursor pagination |
| POST /wallets/{walletId}/reconciliation | Internal | Read-only, consistent-snapshot reconciliation |
| POST /wagering/transactions | Provider | Shared financial use case with persistent idempotency |
| GET /wagering/transactions/{transactionId} | Owning provider | Original result, status and failure code |
| GET /providers/{providerId}/wagering/transactions/{externalTransactionId} | Matching provider | Provider-scoped external lookup |
| GET /metrics | Internal | Reconciliation mismatch counter; broader observability is still pending |

The application authorization checks are repeated inside use cases, not only in
HTTP middleware. Looking up another provider's internal transaction ID returns
404. Explicitly requesting a different provider path or body identity returns 403.

## Input Contract

POST /wallets accepts exactly playerId and initialBalance. Financial submission
accepts providerId, externalTransactionId, playerId, walletId, roundId, gameId,
kind, money and optional referenceExternalTransactionId. Idempotency-Key must be
present exactly once. It is used as received, never silently derived or replaced.

Bodies must be application/json, at most 64 KiB, and valid UTF-8. Duplicate,
unknown and incorrectly cased fields are rejected. IDs use canonical UUID syntax;
uppercase UUIDs normalize to lowercase before domain construction and hashing.
Text business identifiers are limited to 256 UTF-8 bytes without surrounding
whitespace or ASCII controls; idempotency keys permit up to 512 bytes.
An optional reference must be omitted when absent; null and empty strings fail.

Money must use supported currency and a canonical decimal string such as
{"amount":"25.00","currency":"BRL"}. JSON numbers are rejected. OPENING is
not accepted externally. Domain amount and reference policies remain unchanged.

The case-sensitive business strings are not trimmed or case-normalized. The
SHA-256 contract remains the domain's lexical-key JSON encoding. Normalized UUIDs
ensure HTTP and future SQS adapters can construct identical business payloads.

## Responses

| Outcome | HTTP status |
| --- | --- |
| Wallet created | 201, with Location header |
| Processed operation or processed replay | 200 |
| Pending operation or pending replay | 202 |
| Invalid JSON, money, identifiers, operation, cursor or idempotency header | 400 |
| No valid token | 401 |
| Forbidden actor/provider | 403 |
| Missing resource or another provider's transaction ID | 404 |
| Existing wallet, different payload for key, or alternate key for existing external ID | 409 |
| Payload too large | 413 |
| Unsupported content type | 415 |
| Persisted business rejection | 422 |
| Persisted FAILED outcome on submission replay | 500, retryable=false |
| Transient dependency failure or unknown commit outcome | 503, retryable=true |

Transaction submissions return transactionId, status and idempotentReplay. An
observed balance and walletVersion are included when available. Rejected/failed
outcomes include their stable domain failureCode. GET transaction queries return
200 for any persisted status, including REJECTED and FAILED.

No balance is fabricated for WALLET_NOT_FOUND or an invalid wallet context.
Business rejection codes remain the domain's documented codes, including
INSUFFICIENT_FUNDS and the distinct REVERSAL_INSUFFICIENT_FUNDS. Key conflicts use
IDEMPOTENCY_CONFLICT or EXTERNAL_TRANSACTION_CONFLICT. Wallet duplicates use
WALLET_ALREADY_EXISTS. Correctable invalid input is not persisted as a rejection.

Validation/infrastructure errors use:

```json
{"error":{"code":"IDEMPOTENCY_CONFLICT","message":"The operation identity conflicts with an existing request","correlationId":"request-id","retryable":false}}
```

X-Correlation-ID is optional (up to 128 valid UTF-8 bytes). Financial handlers
generate one when absent and return it as a header. Authentication failures retain
the existing generic authentication error format. Tokens, SQL error details and
financial payloads are not logged.

## Persistent Identity and Retry

The shared application service inserts the identity before locking the wallet.
A unique collision leaves SQL usable for inspecting both provider-scoped keys.
Same key/hash returns the stored result. A changed business payload conflicts;
an alternate key for the same external ID also conflicts, even if content matches.

Successful replay performs no wallet, ledger, journal or outbox writes. The
original result survives later movements and a new connection pool/handler.
There is no process-local idempotency map or financial mutex.

A complete SQL callback may retry up to three attempts for deadlock or serialization
failure, with context-aware bounded backoff. Each attempt uses the same generated
transaction, ledger and event identities. Broker publication is not part of that
callback. An unknown commit outcome is not automatically retried; HTTP returns
COMMIT_OUTCOME_UNKNOWN with instructions to reuse the same external identity/key.

## Ledger and Reconciliation

Ledger limit defaults to 50 and must be between 1 and 100. The response has items
and optional nextCursor. A cursor is an opaque base64url continuation bound to
wallet ID, last version and the first page's upper version. Ordering is ascending
walletVersion then ID; new movements do not change an existing page sequence.
The cursor is not an authorization token and does not replace the internal-actor
check. Invalid or cross-wallet cursors fail with 400.

Reconciliation accepts no body and performs one SQL statement so stored balance
and ledger totals share an MVCC snapshot. PostgreSQL NUMERIC sums exact integers,
including opening. Results expose walletId, storedBalance, calculatedBalance,
difference, consistent and checkedEntries. No balance is repaired. A mismatch is
logged with wallet/correlation IDs and increments the low-cardinality counter
wagering_reconciliation_mismatches_total, exposed on the internal metrics route.
Totals outside the representable Money range are returned as an infrastructure
error rather than silently truncated.

## Run and Verify

Apply the existing migrations first, then rebuild the service:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile finance run --build --rm migrate
docker compose up --build -d --wait --wait-timeout 180 app
```

The real PostgreSQL + Keycloak API suite uses the isolated finance-postgres
database. Do not run it simultaneously with finance-tests: both intentionally
reset that same dedicated test database.

```powershell
docker compose -f compose.yaml -f compose.finance.yaml -f compose.api.yaml --profile financial-testing --profile api-testing run --build --rm api-tests
```

The suite gets real client-credentials tokens and exercises the actual financial
handlers, SQL repositories and migrations. It covers provider isolation, opening,
uniqueness, key/hash conflicts, persistent replay through another pool/handler,
pagination, reconciliation, missing wallets, rejection, LOSS, reference wait,
external OPENING rejection and simultaneous duplicate submissions.

For an end-to-end smoke check against the running app, execute the commands in
scripts/smoke-api.ps1 in PowerShell, or run the script if local policy permits:

```powershell
.\scripts\smoke-api.ps1
```

Each run creates a new test wallet and verifies opening 100.00, BET 80.00, WIN
30.00, replay returning the original 20.00 and current balance 50.00. It does not
print tokens or remove database records. Local fixture client secrets are the
defaults; optional environment variables are documented in the script.

## Remaining Work and Evidence

Pending reference operations are persisted with durable work and events but are
not automatically retried until the reference worker is implemented. Likewise,
outbox events accumulate until a publisher is added. SQS consumption, broker
readiness, tracing and three-process/crash tests remain separate deliverables.

This block's preparation includes source syntax and patch checks. Go execution,
the real infrastructure suite and smoke script must pass on the user's machine;
no unexecuted test is claimed as passing.
