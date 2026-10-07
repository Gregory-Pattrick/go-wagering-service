# Performance methodology and evidence

## Status

HTTP benchmark tooling is provided. No measured throughput or latency is claimed
until an actual run completes and its evidence is reviewed. SQS offered load,
asynchronous end-to-end latency and controlled publisher-outage load recovery
remain separate work. The earlier correctness/recovery tests are not benchmarks.

## Reproducible HTTP command (Windows PowerShell)

Start Docker Desktop with Linux containers. From the repository root:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-performance.ps1")))
```

The full script is one block so interactive pasting cannot print a final PASS
after an earlier terminating failure. It saves logs and stops only its isolated
`wagering-performance` project in finally. It never deletes volumes or purges
queues. No Python/k6 installation on Windows is required.

Expected final gate:

```text
PASS: HTTP load scenarios, post-load reconciliation, SQL audits and measured report.
```

The evidence directory is `test-results/performance-<timestamp>`. Open its
`PERFORMANCE.md` and keep all raw JSON files. A report without `result.txt` from
the same run is incomplete. Failure logs must not be turned into passing results.

## Environment

Standalone `compose.performance.yaml` reuses the tested service configurations
but runs normal Go binaries, not race-instrumented ones. Do not merge it with
development Compose. It publishes no host ports and uses separate named volumes.

| Component | Count | CPU limit each | Memory limit each |
| --- | --- | --- | --- |
| API | 3 | 1 CPU | 512 MiB |
| Outbox/reference worker | 2 | 1 CPU | 512 MiB |
| PostgreSQL, Keycloak, MiniStack | 1 each | 2 CPUs | 1 GiB |
| k6 generator | 1 | 2 CPUs | 1 GiB |

The metadata sampler has no explicit CPU/memory cap. These limits are ceilings,
not reservations. The host can become the bottleneck. Close unrelated workloads
and record whether the development stack was left running. Docker allocation,
host CPU/RAM, image versions, commit and dirty files are recorded in
`environment.json`; container image IDs are saved in `images.txt`. Runtime
services, database and load generator run on the same host.

Each API/worker database pool has a maximum of 10 connections. Worker polling is
500 ms, lease 30 s, operation timeout 10 s. Metrics are enabled on APIs with
five-second probes; tracing is explicitly disabled. No input consumers run in
this HTTP-only topology. Outbox events are published to the private output queue;
publication acknowledgment is distinct from downstream consumption.

## Workload

Each of three cases runs a separate 30-second warmup and a 120-second measurement,
with fresh fixture wallets in each invocation. This is one repetition per case;
repeat the complete script for independent evidence directories when time allows.
The six windows total 7.5 minutes before setup, builds, audits and drain waits.

| Case | Workload | Financial check |
| --- | --- | --- |
| many | 50 wallets, unique WIN of 0.01 BRL per request | Balance 100.00 plus one cent per committed movement; ledger/version and double-entry audit |
| hot | One wallet initially 100.00 BRL, unique BET of 1.00 BRL | At most 100 processed bets; later insufficient-funds rejections are expected; balance never negative |
| replay | One seeded BET of 1.00 BRL, repeated identity | Every measured response is an original-result replay; balance 99.00 and version 2 |

Requests rotate across the three API addresses without a load balancer. The hot
case measures successful writes first, then mostly rejection throughput after
funds run out; it is not sustained successful-debit capacity. Monetary request
amounts are fixed decimal strings; verification uses integer minor units.

k6 uses constant-arrival-rate, default 20 iterations/s, 10 preallocated VUs and
maximum 50 VUs, with one measured transaction per iteration. Set `$env:PERF_RATE`
(1..200) before running to change the offered rate; remove it afterward with
`Remove-Item Env:PERF_RATE` to restore defaults. Choose the rate deliberately;
the challenge establishes no minimum RPS. No automated saturation search occurs.

Token acquisition is outside transaction latency, but consumes generator time.
Tokens refresh per VU after 60 seconds. Token errors fail the test. Dropped
iterations remain visible and do not become fabricated successful requests.
No debug HTTP logging or token-bearing headers are exported.

## Measurements and correctness gates

- Raw k6 summaries contain measured transaction p50/p95/p99, counts, durations,
  HTTP status metrics, unexpected errors, expected business rejections, replays,
  dropped iterations and thresholds. Setup/auth/reconciliation requests are not
  included in the custom transaction latency distribution.
- Throughput in the generated table is completed transaction attempts divided
  by configured arrival duration. Completions during the 30-second grace window
  are counted; use raw durations when comparing runs. Replays are responses, not
  unique financial movements.
- Every fixture wallet is read and reconciled through all three API instances
  after traffic. Explicit counters require teardown to complete and the expected
  number of wallets to reconcile. Unexpected outcomes or zero traffic fail.
- SQL then independently checks wallet/ledger balances and versions, accounting
  journal balance, liability accounts, financial uniqueness and absence of money
  movements for non-processed transactions. It requires SQL outbox drain.
- The sampler polls metrics every second; database facts update every five seconds.
  The report selects API 1 for shared outbox facts rather than summing replicas.
  Sampled backlog and age peaks are not exact instantaneous maxima.
- Database conflict counters only count errors exposed after retries. They do not
  measure every SQL retry or time waiting for a lock. Sample errors are not zeros.
- Drain observation begins after teardown and is bounded by sampling resolution.
  This is not per-event latency or a publication-outage recovery benchmark.
- All five Go processes must still be running with no restart/OOM kill. The
  sampler stops before the report is read; the report rejects failed thresholds
  or missing/error metric samples.

## Recording results for delivery

Review the generated report and raw JSON before adding selected sanitized results
to the repository in a later evidence commit. The entire `test-results` directory
is intentionally ignored; do not force-add logs, environment secrets or tokens.
Record the actual tested revision, local uncommitted file list, host contention
and number of repetitions. Do not fill a results table with estimates.

The known MiniStack incorrect-secret signature-validation limitation remains;
a successful load test does not establish SQS authentication correctness.

References:
- https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/constant-arrival-rate/
- https://grafana.com/docs/k6/latest/using-k6/test-lifecycle/
- https://grafana.com/docs/k6/latest/results-output/end-of-test/custom-summary/

## SQS workload companion

[SQS performance methodology](SQS-PERFORMANCE.md) adds a Go producer with separate
send and observed terminal latency, distinct envelopes for application duplicates,
exact inbox completion audits and a controlled output-publication pause. Its
measured evidence is generated separately from the HTTP report; it does not
retroactively change the scope or results of an earlier HTTP run.
