# SQS load and observed processing latency

## Status and command

This block supplies tooling; measured results only exist after a successful
local execution. Run after committing the HTTP performance block:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-sqs-performance.ps1")))
```

It uses standalone `compose.sqs-performance.yaml` under project
`wagering-sqs-performance`, separate volumes, no host ports and normal Go
binaries. It must not be merged with development or HTTP-performance Compose.
The script sets the required evidence-directory variable automatically.

Expected final gate:

```text
PASS: SQS load, observed terminal latency, inbox audits and publication recovery.
```

Evidence goes to `test-results/sqs-performance-<timestamp>` and remains ignored
by Git. Inspect `SQS-PERFORMANCE.md`, `environment.json`, raw phase JSON, inbox
SQL fixtures, metrics.jsonl and result.txt. No tokens or credential files are
included. Do not treat a generator PASS (which says SQL audit pending) as the
whole suite's PASS. Failed runs preserve diagnostics and stop isolated services.

## Workload and environment

The default is 20 scheduled messages/s, configurable with `SQS_LOAD_RATE` (1..100).
The Go generator uses one serial SDK SendMessage call at a time, an 8-second
timeout and one attempt. It schedules fixed slots; if late by more than one slot,
it counts a generator drop rather than bursting to catch up. This is offered load
with a bounded generator, not an unconstrained capacity benchmark.

| Case | Windows | Workload |
| --- | --- | --- |
| many | 30 s warmup, 120 s measurement | Unique WIN 0.01 BRL across 20 wallets |
| duplicates | 30 s warmup, 120 s measurement | Adjacent pairs share business identity; every envelope/message/deduplication ID is different |
| outage | One 20 s measurement | Unique WINs while both output publishers are stopped; consumers remain active |

Each phase creates fresh wallets with 100.00 BRL. All amounts are fixed strings;
expected balances are computed in integer minor units. Warmups are recorded
separately. One repetition per case is the default; no minimum throughput is
required by the challenge. Scheduled traffic totals 5 minutes 20 seconds plus
builds, setup, polling, audits, recovery and drain waits.

Three APIs, two publishers and two consumers run with 1 CPU/512 MiB limits each.
PostgreSQL, Keycloak and MiniStack retain the HTTP block's 2 CPU/1 GiB ceilings.
The Go generator has 2 CPUs/512 MiB. The Python sampler has no explicit cap.
API pools have at most 10 connections per process; worker polling is 500 ms.
The same Go version and pinned AWS SDK in go.mod are used. Image build runs
producer unit tests with -race, then builds measured binaries without -race.
Tracing is disabled; API readiness/metrics are enabled with five-second probes.

The producer receives only its dedicated producer credential file. It uses no
ambient admin credential chain. Both consumers use their separate least-privilege
consumer credentials. Runtime applications receive no administrator key.
Generator UID 0 is used only for writing benchmark artifacts to the Windows bind
mount; application roles remain non-root. Metadata records host/Docker CPU and
memory, revision, dirty files and generator settings.

## What the timing means

- Send latency: one SDK SendMessage call, including transport response time.
- Observed terminal latency: time from the first successful send attempt's start
  for a business identity until HTTP first returns PROCESSED for that identity.
  Time uses Go's monotonic clock in one producer process.
- The independent observer issues at most 16 concurrent HTTP lookups, waits
  250 ms between rounds and uses five-second HTTP timeouts. Observer scheduling,
  API latency and polling intervals add delay. Reported terminal latency is an
  upper bound on commit latency, not an exact server execution measurement.
- Duplicate-envelope completion is not assigned the original operation's latency.
  Unique operations have one terminal observation; every accepted envelope must
  independently have an inbox row confirmed by the later SQL audit.
- The producer has no unbounded retry loop: send/poll errors fail the run, even
  if processing later succeeds. A 404 before the consumer commits is expected.
- Terminal drain is measured from the last send return until all accepted unique
  operations are observed. The observer has a 180-second drain budget; in-flight
  requests may take their bounded timeout to terminate.
- Quantiles use nearest-rank on raw observations, unlike k6's HTTP summary
  estimator. Do not compare them as identical estimators. Raw arrays are retained.

## Financial and recovery gates

For each unique accepted business identity, the final wallet must have exactly
one additional cent and one additional version/ledger entry. All wallets are
read and reconciled through all three APIs. Each accepted message ID must appear
in the `wager-transactions-v1` inbox; the SQL fixture waits at most 90 seconds for
those rows. Output publication and the existing independent SQL accounting audit
must pass, including uniqueness and no money movements for rejected transactions.

The duplicates case must accept more envelopes than unique operations. Unique
MessageDeduplicationId values ensure application deduplication is exercised rather
than relying on the FIFO transport deduplication window. The benchmark does not
claim to expire the FIFO window or measure each redelivery's processing latency.

Before the run and after phases, fresh approximate input/DLQ depths are checked.
A nonempty DLQ fails; nothing is purged. Queue depths supplement the exact inbox
fixture checks, not replace them. The report uses only API 1 for shared database
and queue facts to avoid double counting across API replicas.

For outage, both publishers are stopped, the generator verifies financial
processing through SQS, and a fresh positive outbox backlog must be observed.
The controller restores publishers in finally, waits for sampled drain, and runs
SQL/inbox audits. The reported recovery interval begins after the restart command;
startup/controller overhead and five-second probe resolution are not per-event
publication latency. The publisher pause includes fixture setup, the 20-second
send window and final reconciliation, so its total length exceeds 20 seconds.

All seven Go processes must still be running, without automatic restarts or OOM
kills. An intentional docker stop/up of publishers is expected in the outage
case. Evidence is saved before the isolated project stops; volumes survive.

## Limits and remaining delivery work

The HTTP observer itself loads the APIs/database. Generator, application and
emulator share one host; results apply to that setup and recorded workload.
Metric sampling captures approximate peaks. API conflict counters do not expose
consumer-internal SQL retries; consumer conflict/lock-wait rates are not claimed.
No exact per-message DeleteMessage latency or downstream output-event consumer
latency is measured. The emulator's documented signature-validation limitation
remains a separate security issue.

Measured evidence still needs review, sanitization and a final evidence commit.
The requirement-by-requirement review and clean-checkout validation remain pending.
