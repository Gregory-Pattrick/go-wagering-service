# Durable reference and outbox workers

The `cmd/workers` process runs reference recovery and committed-event publication
as two independently cancellable loops composed through Uber Fx. It exposes no
HTTP listener and requires neither Keycloak tokens nor administrator database
credentials. The API remains a separate process.

## Reference recovery

The API persists `PENDING_REFERENCE` and its first event in the financial SQL
transaction. The worker claims due rows with `FOR UPDATE SKIP LOCKED`, increments
the durable attempt counter and commits a fresh, unique lease token. Database
time governs leases and scheduling.

A second SQL transaction locks the work row, transaction and wallet, rechecks the
lease after waiting for the wallet, and invokes the same domain evaluator used
by the API. A successful outcome commits the wallet, immutable ledger, balanced
journal, terminal transaction, events and removal of work together. Other wallet
writers remain serialized by the wallet row lock, independently of SQS ordering.

Missing or still-pending eligible references are rescheduled. They do not move
money or repeatedly emit PendingReference. A rejected or failed eligible reference
causes `REFERENCE_NOT_PROCESSED`; identity, amount and kind mismatches retain the
domain-specific rejection codes. A deadline reached while the reference remains
unresolved produces `REJECTED / REFERENCE_NOT_FOUND` and one rejection event. This
code covers both a reference that never arrived and one that remained pending.
Expiry takes precedence over a reference arriving after the deadline.

The persisted deadline is 15 minutes from transaction creation. `REFERENCE_TTL`
can shorten it for tests but cannot extend it. Infrastructure failures roll back
the attempt and retain durable work; they do not turn a transaction into FAILED.
An unresolved reference can be expired once the database is available again.
Replays after completion return the persisted terminal result without another
financial effect.

## Event publication

Claims are short committed SQL operations; no SQS request runs while database
locks are held. Unpublished events for each wallet are selected in persisted
`(occurred_at, event_id)` order. A leased or delayed earlier event blocks later
events for that wallet, while different wallets can be claimed independently.
Ties between events with the same occurrence time are broken by event ID; the
two events of one financial operation have no specified relative order.

The sender uses the official AWS SDK for Go v2, with:

- `MessageGroupId = aggregateId` (wallet ID).
- `MessageDeduplicationId = eventId`.
- Message body equal to the immutable JSON snapshot read from the outbox.
- One SDK attempt per durable attempt; retry scheduling belongs to PostgreSQL.

After a successful send, confirmation checks the current lease token and deadline.
A crash before sending leaves an expiring lease. A crash after sending but before
confirmation can cause another send, preserving the event ID and payload. Delivery
is **at least once**. FIFO deduplication has a finite window; downstream consumers
must also deduplicate persistent event IDs.

Transient and permanent publication errors retain the event with bounded backoff.
There is no automatic discard of committed financial events. A persistent IAM or
payload error requires operator correction and can block that wallet's event
stream. Input-message redrive and the SQS consumer are a subsequent block.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| WORKER_POLL_INTERVAL | 500ms | Idle/error polling pause |
| WORKER_LEASE | 30s | Claim duration, at least twice the operation timeout |
| WORKER_OPERATION_TIMEOUT | 10s | Maximum time for one worker step |
| WORKER_RETRY_BASE | 1s | Minimum retry delay |
| WORKER_RETRY_CAP | 60s | Maximum retry delay |
| REFERENCE_TTL | 15m | Maximum age of unresolved reference work |
| AWS_REGION | Required | Signing region |
| SQS_ENDPOINT | Required | Local SQS endpoint |
| SQS_EVENTS_QUEUE_URL | Required | Output FIFO queue URL |
| SQS_PUBLISHER_CREDENTIALS_FILE | Required | Dedicated publisher JSON key file |

Backoff is exponential with equal jitter, bounded by base and cap. Operational
settings are process environment variables. Keep the production defaults for
reference age; only shorten the TTL in disposable tests.

`publisher-init` verifies the existing publisher IAM policy and stores one access
key in a named volume. The non-root worker gets that volume read-only. It never
uses the administrative provisioning key or an ambient AWS credential chain.
Keys are local development fixtures; production credential rotation and workload
identity are not implemented. Recreate workers after rotating their key file.
Never commit key files or copy their contents into logs.

MiniStack's previously documented wrong-secret/SigV4 limitation still applies.
This block does not claim that the emulator fully authenticates AWS signatures.
See `docs/sqs.md` for the retained security checks and limitations.

## Run

Apply migrations using the existing financial setup before starting workers.
From the repository root:

```powershell
docker compose -f compose.yaml -f compose.workers.yaml --profile workers up --build -d workers
docker compose -f compose.yaml -f compose.workers.yaml --profile workers logs --tail=40 workers
```

`workers started` confirms both loops started. Successful sends log
`outbox event published` with event ID and attempt count. Reference attempts log
transaction ID, status and attempt count. Errors log a safe category; integration
tests report the underlying failure for diagnosis without logging tokens.

Run `scripts/smoke-workers.ps1` with the API running. If PowerShell blocks script
files, open the file in VS Code and paste its contents into the terminal. This
creates a fresh wallet and performs REFUND-before-BET, terminal replay,
reconciliation and an outbox check for seven acknowledged events. It does not
consume or delete output SQS messages.

Multiple worker processes can share the database:

```powershell
docker compose -f compose.yaml -f compose.workers.yaml --profile workers up -d --scale workers=2 workers
```

Stop them with the same Compose file set:

```powershell
docker compose -f compose.yaml -f compose.workers.yaml --profile workers stop workers
```

Fx cancels both loops and waits for them before closing PostgreSQL. Interrupted
work remains recoverable after its lease expires. Do not remove database or SQS
volumes to recover leases.

## Verification and limits

```powershell
go test -count=1 ./...
go vet ./...
go build ./...
docker compose -f compose.yaml -f compose.finance.yaml -f compose.workers.yaml --profile financial-testing --profile worker-testing run --build --rm worker-tests
```

The integration suite resets only the dedicated `finance-postgres` database.
Do not run it concurrently with finance-tests or api-tests. Real SQS tests publish
fresh test events to the local output queue; they do not purge existing messages.

Coverage includes repeated waiting, late references, pending/rejected/mismatched
references, expiration, a fresh database pool, exclusive reference claims, two
publisher claims, stale-token fencing, retry scheduling, and real SQS sends with
an intentionally omitted database acknowledgment. Test-only SQL moves lease
clocks instead of fragile sleeps. This models crash boundaries; it is not yet
an OS-level SIGKILL/multi-process crash test. The real SQS case verifies accepted
sends and SQL confirmation, not downstream consumption or duplicate delivery
beyond the FIFO deduplication window. Those checks remain in the resilience block.

Worker readiness, metrics, tracing and the input consumer are implemented in
separate modules. The unified startup in README.md enables them. API liveness
alone does not establish worker readiness.
