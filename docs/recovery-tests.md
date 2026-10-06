# Controlled process interruption tests

This suite uses actual Docker `SIGKILL`, not an error returned by a mock. It is
separate from the concurrency suite and runs under the `wagering-recovery`
Compose project with its own PostgreSQL, MiniStack, credentials and volumes.
No host ports are published. Do not merge its Compose file with development.

## Test-only instrumentation

`cmd/crashprobe` and `internal/testsupport/crashprobe` require the
`faultinjection` Go build tag. Normal application, worker and consumer builds
cannot import or include these files. `Dockerfile.recovery` builds a separate
race-instrumented executable and runs the probe unit tests first.

The probe executable composes the existing production `workers.Runner`,
`workers.Consumer`, SQL repositories, shared financial use cases and official
SQS SDK adapters. Wrappers pause immediately around the real send/delete/claim
boundaries. There is no replacement implementation of financial processing.
The suite uses a separate test composition; normal production Fx wiring still
needs its existing lifecycle/integration tests.

A wrapper atomically writes a small marker containing identity, attempt number,
claim owner when applicable, and payload SHA-256. It then blocks. The host waits
for that marker, independently checks committed SQL state, and sends SIGKILL.
Marker files do not contain receipt handles, tokens, credentials or full event
payloads. Only the fixture file contains synthetic local financial test data.

The barrier deliberately ignores operation-context cancellation, so it cannot
accidentally cross the target boundary before being killed. A three-minute
watchdog panics if the controller never sends SIGKILL. A watchdog failure is a
failed test, never evidence of the requested interruption. No control listener
or remotely callable fault endpoint exists.

## Scenarios

| Scenario | Evidence required before kill | Recovery assertions |
| --- | --- | --- |
| SQL commit before input deletion | Inbox row and financial movement committed; delete wrapper blocked | Broker message ID and envelope unchanged; receive count increases; one inbox row and debit; successful deletion observed |
| Outbox claim before send | Target event committed, claimed and unpublished; sender blocked before network call | New claim owner, increased attempt, same event ID and payload hash; target independently received from SQS |
| SQS send before outbox confirmation | SDK send succeeded, target outbox row still unpublished | Target received from SQS before takeover; another publisher claims and confirms the same event ID/payload |
| Reference claim before processing | PENDING_REFERENCE and durable work claim exist; no refund movement | Later BET arrives; new worker claims after lease expiry and processes the refund once; work removed |

The publisher checkpoints target `WagerTransactionProcessed` for a specific
transaction. Other events, such as opening or balance events, may already be
published. This does not assert that the entire wallet's stream is unpublished.
Both recovery publishers run concurrently; either can take over. Fair allocation
of work is not required. The default 30-second leases/visibility remain enabled;
there is no SQL update that artificially expires a lease, and no arbitrary
sleep that substitutes for observing completion.

After recovery the suite reconciles the wallet, verifies replay identity,
checks ledger and double-entry invariants, verifies terminal event uniqueness
and drains the outbox. It then restarts all active Go processes while retaining
PostgreSQL and SQS volumes and repeats the read/replay/SQL assertions.

## FIFO semantics

Resending an accepted event with the same deduplication ID inside the FIFO
window may not create a second physical message. The suite compares stable
identity/payload across actual successful send attempts and proves at least
one independent broker receipt. It does **not** claim exactly-once delivery or
prove duplicate behavior after the broker deduplication window expires.

The output observer acknowledges test messages on this project's private output
queue so FIFO groups can advance. It never accesses development queues. The
observer alone uses local administrator credentials for assertions; publishers,
consumers and the input producer keep their dedicated restricted credentials.
The known MiniStack signature-validation limitation remains unchanged.

## Windows execution

Prerequisites: all prior blocks applied and validated, including the distributed
suite. Start Docker Desktop with Linux containers and open the repository root.

```powershell
docker compose -p wagering-recovery -f compose.recovery.yaml config --quiet
code .\scripts\test-recovery.ps1
```

Copy the entire script into PowerShell if script execution is disabled. When
execution is already allowed, run `./scripts/test-recovery.ps1` directly.
The script builds the images, provisions credentials, applies migrations and
runs all four scenarios. It stops this isolated project in `finally` and never
removes volumes. Allow time for the real visibility/lease expiry in each case.

Only this final line means the full suite passed:

```text
PASS: four controlled SIGKILL scenarios, durable recovery, SQL audits and process restarts.
```

`test-results/recovery-<timestamp>` contains container states, per-scenario logs,
probe metadata and the final result. An interrupted or failed run has no final
PASS file. Some archived entries in a reused control volume may come from a
previous run: compare timestamps and require the current run's final result.
Never report prepared scripts or static checks as successful execution evidence.

To stop after an interrupted terminal session:

```powershell
docker compose -p wagering-recovery -f compose.recovery.yaml stop
```

Inspect failures before rerunning. Do not purge queues or delete database volumes
to hide a failed financial assertion. These tests cover process crashes, not
host power loss, filesystem durability, network partitions, or arbitrary fault
interleavings. Load measurements and tracing remain separate delivery gates.
