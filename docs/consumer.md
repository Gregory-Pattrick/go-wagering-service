# SQS consumer and transactional inbox

`cmd/consumer` is a separate, non-root Fx process. It consumes
`wager-transactions.fifo` and invokes the same `financial.Service.Submit` use case
as the HTTP API through an inbox-aware transaction decorator.

## Trust boundary

Only the trusted internal integration producer receives permission to SendMessage
on the shared input queue. External providers must not receive that key. Before
publishing, the trusted producer must establish the provider identity from its
authenticated source. The JSON providerId is routing data within this trust
boundary; possession of an arbitrary providerId is not proof of authentication.

`SQS_ALLOWED_PROVIDERS` defaults to `provider-a,provider-b`. The consumer rejects
unknown providers. It does not validate an OIDC token embedded in a message.
The HTTP API continues to authenticate requests with Keycloak.

Separate IAM identities and named volumes are used for the producer and consumer.
The consumer cannot read the producer key. Its queue policy permits receive,
delete, visibility changes and queue metadata; it cannot publish financial input
or output. Administrative credentials occur only in initializers and the smoke
observer. The unmodified MiniStack image accepts incorrect secrets. The custom signature
gate addresses this at the local broker boundary; see broker-authentication.md
for supported protocols and the outstanding runtime checks.

## Input contract

```json
{
  "messageId": "integration-envelope-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-10-06T12:00:00Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "external-123",
    "idempotencyKey": "provider-a:external-123",
    "playerId": "00000000-0000-4000-8000-000000000001",
    "walletId": "00000000-0000-4000-8000-000000000002",
    "roundId": "round-123",
    "gameId": "game-123",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}
```

Use real player and wallet IDs in requests. Reference operations additionally use
`data.referenceExternalTransactionId`. OPENING is not accepted on this transport.
The key is exactly `data.idempotencyKey`, never a generated substitute.

The envelope must be UTF-8 JSON, at most 64 KiB, with exact field names and no
unknown or duplicate fields. Money uses canonical decimal strings. occurredAt
must be RFC3339 with a zero UTC offset. IDs and monetary business rules are also
validated by the shared financial use case. The input has no version field in
the challenge contract; the consumer name identifies this contract version.

`MessageGroupId` must equal the canonical walletId. Deduplication IDs belong to
transport sends, not to financial correctness. Repeated delivery tests use fresh
transport deduplication IDs so SQS FIFO does not suppress the test input itself.

## Atomicity and replay

The inbox primary key is `(consumer_name, envelope.messageId)`, using consumer
name `wager-transactions-v1`. The SQS delivery ID and receipt handle are not inbox
identities. The hash is SHA-256 over the validated, canonically serialized envelope:
JSON whitespace/key order and equivalent UTC formatting do not affect it; wallet
and player UUIDs are normalized. Money is canonically serialized. The hash includes
the envelope's messageId, type, occurredAt and business data. Changing that content
requires a new messageId. Business payload hashing remains the existing domain hash.

A transaction-scoped PostgreSQL advisory lock serializes one consumer/envelope
pair before an inbox row exists. A hash collision can only add contention: the
actual consumer/message key and payload hash are still checked exactly. There is
no global lock and no local mutex relied on for correctness.

Within the same SQL transaction, the decorator:

1. Acquires the per-envelope lock and verifies any existing inbox hash.
2. Runs the shared financial callback, including dual persistent idempotency.
3. Inserts inbox completion referencing the resulting financial transaction.
4. Commits financial state, ledger, journal, outbox and inbox together.

A same-messageId/different-hash delivery has no financial effect and is retained
for broker redrive. New message IDs with the same business identity complete new
inbox rows while replaying the original transaction. HTTP and SQS races resolve
through the same database unique constraints and wallet row lock.

There is no early committed "in progress" inbox record. Failure before completion
rolls back the whole unit. An unknown commit result causes redelivery with the
same identity; it does not trigger an unconditional new financial operation.

## Acknowledgment, retries and DLQ

The consumer deletes a receipt only after the financial application returns a
successful commit. PROCESSED, REJECTED and durably PENDING_REFERENCE all qualify.
Pending references are then owned by the durable reference worker. Business
rejections are completed outcomes, not poison messages.

Malformed JSON, unsupported operations, missing keys, hash/idempotency conflicts,
unknown providers and transient failures are retained. Visibility is changed with
exponential equal-jitter backoff from 1 to 60 seconds. The existing broker redrive
policy moves messages after its maxReceiveCount threshold of 5; the consumer does
not invent a second manual DLQ send/delete protocol. A prolonged infrastructure
outage can exhaust deliveries and requires later DLQ investigation/redrive.

Receive long polling is 10 seconds. Each receive has a 15-second context; financial
processing and acknowledgment share a 10-second context after receive. Initial
visibility is 30 seconds. There is one in-flight message per consumer process;
scale independent processes for concurrency. No visibility renewal is necessary
for the bounded 10-second attempt. A failed visibility change leaves the original
broker timeout available for recovery.

SIGTERM cancels receive and active processing. The SQL unit rolls back if it has
not committed; the consumer attempts to release its receipt with visibility zero
using a separate two-second cleanup context. If commit succeeded but delete did
not, inbox/financial replay protects redelivery. Fx waits for the loop to finish
before closing the database. OS-level SIGKILL tests remain a later resilience step.

## Configuration and execution

| Variable | Meaning |
| --- | --- |
| DATABASE_URL | Application-role PostgreSQL URL |
| AWS_REGION | SQS signing region |
| SQS_ENDPOINT | Broker endpoint |
| SQS_INPUT_QUEUE_URL | Input FIFO URL |
| SQS_CONSUMER_CREDENTIALS_FILE | Consumer-only JSON key file |
| SQS_ALLOWED_PROVIDERS | Comma-separated provider IDs; default provider-a,provider-b |

After validating the durable-workers block and applying migrations:

```powershell
docker compose -f compose.yaml -f compose.consumer.yaml --profile consumer up --build -d consumer
docker compose -f compose.yaml -f compose.consumer.yaml --profile consumer logs --tail=40 consumer
```

`consumer started` confirms lifecycle startup, not successful consumption. Run:

```powershell
docker compose -f compose.yaml -f compose.consumer.yaml --profile consumer run --rm consumer-smoke
```

This test requires the input queue and its DLQ to be empty. It refuses to purge
existing data. It creates a fresh wallet, publishes with the producer key, checks
HTTP replay and duplicate sends, verifies an insufficient-funds rejection, then
observes malformed input actually arriving in the DLQ. The administrative observer
deletes only that known test poison message after verification. Output events are
left for the separately running outbox publisher and downstream consumers.

## Integration coverage

```powershell
go test -count=1 ./...
go vet ./...
go build ./...
docker compose -f compose.yaml -f compose.finance.yaml -f compose.consumer.yaml --profile financial-testing --profile consumer-testing run --build --rm consumer-tests
```

The database suite resets only finance-postgres and must not run concurrently
with other suites using that container. It checks contention between the HTTP and
inbox application entry points, replay after later balance changes, rollback
between financial writes and inbox completion, completed pending/rejected inbox
records, payload conflicts and concurrent duplicate envelopes. The real HTTP/SQS
smoke is separate from these database tests.

Queue-depth attributes are approximate and used for bounded smoke polling, not
as the correctness mechanism. SQL integration assertions verify exact inbox and
ledger counts. Readiness, complete telemetry, multi-process kill tests and load
reports remain subsequent work. Do not mark this block validated until the Go,
container integration and smoke commands succeed in the execution environment.
