# Architecture

## Composition and process boundaries

A modular Go application exposes three entrypoints: `cmd/service` (HTTP),
`cmd/workers` (reference recovery and outbox publication) and `cmd/consumer`
(SQS ingestion). They share domain/application packages and PostgreSQL state.
Separate executables replace the original plan's role-selected executable; the
challenge does not require a single binary. No financial correctness guarantee
relies on process-local memory or a particular replica surviving.

Uber Fx composes configuration, pgx pools, repositories, use cases, adapters and
workers through constructors, modules and lifecycle hooks. Configuration and
initial dependencies are validated before serving work. Worker loops have
cancellable contexts and bounded operation times. Shutdown stops new intake,
cancels or finishes in-flight work, waits for loops and closes dependencies in
reverse lifecycle order. Unacknowledged work remains durable and recoverable.
The domain imports neither Fx nor HTTP, SQS or persistence libraries.

The canonical local command in README enables the complete Compose file/profile
set. Its migration prerequisite applies all three schema versions before business
processes start. Readiness probes PostgreSQL and SQS; liveness only checks the
process. Telemetry must be enabled for the complete readiness implementation.

## Money, wallet and accounting

Money stores exact int64 minor units and a supported ISO currency (BRL/USD).
External input accepts nonnegative canonical decimal strings with exactly two
fractional digits. It rejects exponents, non-finite values, extra scale and
redundant leading zeros. Arithmetic checks overflow and currency compatibility,
including subtraction and MinInt64 negation. Internal differences may be negative;
a wallet balance cannot. Persistence uses BIGINT, never a monetary float.

Wallets are unique by player/currency. A positive opening creates OPENING,
ledger, double-entry journal and two events at wallet version 1 in one commit.
A zero opening creates no financial entry/event. Later balance changes increment
the version; LOSS preserves balance/version and emits only the processed event.

Ledger entries form an immutable versioned balance chain. Unique indexes, checks,
triggers and restricted role permissions protect nonnegative balances, identity,
ledger immutability and atomic matching of wallet/transaction/ledger/outbox state.
Migration 002 adds a balanced two-posting journal. Clearing accounts do not have
a shared mutable balance, avoiding a global financial bottleneck.

## SQL boundary and concurrency

pgx v5 with explicit SQL keeps transaction boundaries and locks visible.
`Store.Within` owns a READ COMMITTED transaction, and Unit methods share that
transaction. Identity insertion precedes a wallet SELECT FOR UPDATE. Writers
also check the previous version. Reference and compensation checks run inside
the locked-wallet transaction. Independent wallet rows can advance concurrently.
Deferred constraints reject incomplete financial changes at commit.

The application retries only classified deadlocks/serialization failures, at
most three attempts. It does not retry arbitrary callbacks after ambiguous
commit results. Unknown commit outcomes require the original operation identity
and key on retry. Reconciliation uses a read-only REPEATABLE READ snapshot and
rebuilds the balance from the ledger, reporting discrepancies without repair.

## Transaction identity, state and references

External transactions begin PENDING, then become PROCESSED, REJECTED,
PENDING_REFERENCE or FAILED. Terminal states cannot transition. Ordinary
submissions decide inside one transaction rather than committing a separate
asynchronous acceptance. Every committed pending state requires durable work.

A SHA-256 hash covers sorted canonical business JSON; transport metadata and the
idempotency key are excluded. HTTP and SQS normalize UUIDs and use the same domain
input/hash. Unique provider/key and provider/external-ID indexes survive restarts.
Same-key/same-payload requests replay the persisted original result, including
the original balance. Changed payload returns conflict. Another key for an
existing external identity also conflicts, even if its content is equivalent.

Missing/nonterminal references persist a wait and durable schedule. Workers
retry with capped exponential backoff and a finite TTL (15 minutes by default).
Expiry produces REJECTED/REFERENCE_NOT_FOUND and an outbox event. A rejected or
failed reference causes REFERENCE_NOT_PROCESSED. No financial movement occurs
while waiting. See docs/transactions.md and docs/processing.md for failure codes.

REFUND and ROLLBACK require the full referenced amount and matching provider,
player, wallet, currency and round. One BET may receive one successful direct
compensation: REFUND or ROLLBACK. Rolling back a REFUND does not reopen the BET's
compensation right. The database's unique compensation index enforces this policy
across processes. A reversal debit without funds uses a distinct failure code.

## Authentication and authorization

Keycloak provides OAuth2/OIDC client credentials and a versioned imported realm.
The verifier checks RS256 signature, issuer, audience, token type, expiration,
not-before, issued-at and explicit allowed client/actor/provider claims. The
application neither stores passwords nor issues its own access tokens.

Provider identity comes from the verified token, never a request body. Actor
checks and provider ownership precede identity lookup and replay. Internal wallet
routes require the wallet-service identity. Provider transaction queries include
provider scoping, including queries by internal transaction ID.

Initial JWKS unavailability prevents startup. Cached keys can remain usable
through an IdP outage. Runtime JWKS failures currently map to 401 rather than a
separate 503. Immediate revocation/introspection is not implemented. Keycloak's
local development database is ephemeral; production needs durable IdP storage,
TLS and managed secrets.

The shared SQS input is writable only by a trusted internal producer with a
restricted IAM policy. Game providers do not receive shared-queue credentials.
Within that trust boundary, providerId is routing data; the consumer still checks
allowed providers, envelope shape and every financial domain invariant.

Unmodified MiniStack does not validate general SigV4 signatures. The local
Dockerfile.broker adds a narrow signature gate before MiniStack's IAM evaluation
and SQS dispatcher on the same listener. It preserves role-specific credentials
and rejects unsupported paths/protocols. See docs/broker-authentication.md for
constraints and verification status; this is not a production security gateway.

## Inbox, outbox and recovery

The inbox identity is `(consumerName, messageId)` plus a canonical envelope hash.
A transaction-scoped advisory lock serializes that envelope identity; unique
business identities and wallet row locks protect the financial operation.
Inbox completion shares the transaction with domain, ledger and outbox changes.
SQS deletion follows commit. Business rejections are terminal and acknowledged;
invalid or transient failures are retained for retry/redrive. Pending references
may be acknowledged after the durable reference job is stored.

Input FIFO uses wallet ID as MessageGroupId and an explicit transport deduplication
ID. Application correctness does not rely on FIFO's deduplication window.
Visibility/retry policy and actual DLQ movement are documented in docs/consumer.md.

Outbox events are typed, versioned, immutable snapshots stored in the financial
commit. Publishers claim with SKIP LOCKED and fresh lease tokens, then perform
network I/O outside the SQL transaction. Confirmation requires the live lease.
An interruption after send but before acknowledgment permits republication with
the same event ID. This is at-least-once delivery, not exactly-once publication.
References use the same durable claim/fencing pattern and are re-evaluated under
the wallet lock. Cancellation leaves leases or receipt visibility recoverable.

## Observability and evidence

JSON operation logs carry the available correlation, message, transaction,
wallet and provider identifiers; outbox logs add event and causation identifiers.
Success is recorded after durable financial completion or outbox confirmation.
They exclude tokens, credential files, monetary values and complete payloads.
See docs/operation-logs.md for transport and replay semantics.

Metrics cover outcomes, duplicates, retries, surfaced conflicts, queue/DLQ depth,
outbox age, latency, dependency health and reconciliation mismatches. Shared
state gauges must not be summed across replicas. Metrics do not measure every
SQL lock wait. Tracing propagates W3C context through SQS/outbox metadata; bounded
exporters and collector outages do not determine financial readiness.

The distributed suite uses three independent APIs, two publishers and two
consumers with the race detector. Controlled SIGKILL tests target four durable
boundaries. Separate load suites record workload, latency, errors, accounting
audits and environment provenance. See docs/distributed-tests.md,
docs/recovery-tests.md and both performance methodology documents.

The final delivery must include actual runtime evidence and clean-checkout
validation. Prior load results describe the pre-signature-gate broker. Current
candidates and open gates are recorded in docs/DELIVERY-REVIEW.md. Generated tests,
source review and previous-version PASS reports are not proof that an amended
checkout has passed. No unconditional correctness guarantee is claimed.
