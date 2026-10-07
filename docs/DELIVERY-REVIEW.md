# Delivery review — 2026-10-07

## Scope and evidence

Reviewed the supplied `go-wagering-service-final-review.zip` against the original
challenge and execution plan. This is a source review, not a security certification
or a claim that every possible execution interleaving has been tested.

The author previously reported successful unit, integration, race, distributed,
recovery, telemetry, tracing, dashboard and load runs. HTTP and SQS load summary
reports and their environment metadata were supplied separately. Those results
must retain their original tested revision and uncommitted-file metadata.
They do not establish that an amended checkout has passed.

The review environment parsed 105 Go files, 18 Python files, three JSON files and
the YAML files successfully. Go, Docker and PowerShell were unavailable here;
no new compilation, container execution or clean-checkout PASS is claimed.
YAML parsing does not validate Compose merges or enabled-profile dependencies.

## Findings requiring closure

| ID | Priority | Finding and source | Closure criterion |
| --- | --- | --- | --- |
| R1 | High | MiniStack accepts a known access key with an incorrect secret. `deploy/sqs/verify.py`, `docs/sqs.md`, `docs/sqs-iam.md`. Policy enforcement does not prove possession of the secret. | Demonstrate valid requests succeeding and invalid signatures failing at the actual broker access boundary; retain least-privilege denial checks. Keep the failing verification visible until resolved. |
| R2 | High | The initial README startup path runs base Compose without migrations, workers, consumer or telemetry. Base API startup can succeed against an empty schema; financial requests then fail. `/health/ready` is installed only when observability is enabled. | Make the primary documented startup path apply migrations and start all required roles with PostgreSQL/SQS readiness. Validate that path with fresh project volumes and authenticated HTTP/SQS smoke tests. |
| R3 | Medium | Successful HTTP logs contain route/status/correlation but omit transaction, wallet and provider IDs. SQS logs contain message/delivery IDs but omit the financial identity mapping. `internal/adapters/http/observation.go`, `internal/adapters/http/financial.go`, `internal/adapters/sqs/handler.go`, `internal/workers/observation.go`. | Log available operation identifiers at the application/transport boundaries, including persisted transaction IDs after success, without credentials or complete financial payloads. Verify both HTTP and SQS examples. |
| R4 | Medium | Both duplicate-request suites used twelve concurrent requests; section 13 requires fifty. `internal/adapters/http/financial_integration_test.go`, `deploy/distributed/run.py`. | This review patch changes both to fifty and adds explicit distributed-report counts. Rerun both suites; require one original response, forty-nine replays, one transaction and one debit. Previous PASS results do not close the new test. |
| R5 | Medium | README, ARCHITECTURE and several component documents still describe implemented features as future work. The main host example also omits required OIDC settings. | Consolidate current execution instructions and remove contradictory development-stage statements. Include reversal instructions and all required test profiles. |
| R6 | Medium | Load methodologies are committed, but supplied measured reports are outside this archive. Final fresh-checkout validation has not been supplied. | Commit selected sanitized evidence with original provenance; preserve raw results separately. Run the final documented workflow from a clean checkout and record its exact revision. |

R1 is corroborated by the emulator's documented distinction between IAM policy
checks and signature authentication:
https://ministack.org/blog/changelog-v1-5-0
The actual incorrect-secret acceptance on the pinned version was observed in the
author's earlier run. This review does not assume an emulator upgrade fixes it.

## Requirement mapping

“Present” below means implementation and relevant tests were located, not an
unconditional certification of correctness. Open findings take precedence.

| Challenge sections | Implementation/evidence located | Review status |
| --- | --- | --- |
| 1, 4 — Go, Fx, PostgreSQL, SQS and lifecycle | `go.mod`, Dockerfiles, `internal/bootstrap`, process and worker lifecycle tests | Present; primary startup instructions need R2/R5 |
| 2 — HTTP authentication and authorization | OIDC verifier, actor guards, provider-scoped application queries, real-Keycloak API tests | Present; runtime JWKS failures map to 401, documented limitation |
| 2 — Broker access controls | Dedicated producer/consumer/publisher/monitor credentials and IAM policies | Partial demonstration: R1 remains open |
| 5, 6.1 — Exact money | Encapsulated minor-unit Money, overflow checks, strict decimal parsing; BIGINT persistence | Present; no monetary float conversion found in reviewed paths |
| 5, 6.2, 6.4 — Wallet and immutable ledger | Wallet row locks, version updates, unique identities, nonnegative checks, immutable-row/truncate guards and deferred SQL constraints | Present; database integration coverage located |
| 6.3, 7 — Transactions and reversals | Domain transitions, original-result snapshots, reference context checks, compensation uniqueness, durable reference TTL/backoff | Present; no separate asynchronous acceptance commit in ordinary submission |
| 6.5, 10 — Inbox | Envelope hash, consumer/message identity, same financial SQL transaction and delete after commit | Present; broker identity trust depends on R1 |
| 8, 13 — Distributed concurrency | Three APIs, two publishers, two consumers; overspending, compensation and mixed HTTP/SQS tests | Present; required fifty-request test awaits new execution under R4 |
| 9 — HTTP contracts | Wallets, ledger cursor, provider-scoped reads, submissions and snapshot reconciliation | Present; readiness needs telemetry startup under R2 |
| 11 — Outbox and typed events | Immutable event snapshots, leased SKIP LOCKED claims, fenced confirmation and retry | Present; at-least-once publication is explicit |
| 12 — Observability | Prometheus metrics, JSON logs, dependency probes, reconciliation counter | Present with identifier gap R3; metric limitations documented |
| 13 — Recovery and real infrastructure | PostgreSQL/Keycloak/MiniStack suites, four controlled SIGKILL windows, restart/replay and SQL audits | Present; prior PASS reported by author, final checkout still pending |
| 14 — Optional double-entry accounting | Migration 002, balanced journal/posting constraints and audits | Present |
| 14 — Optional tracing and dashboards | OpenTelemetry, Collector/Tempo, Prometheus/Grafana and live smoke scripts | Present; prior PASS reported by author |
| 14 — Optional HTTP/SQS load | k6, Go producer, isolated topologies, measured quantiles, SQL audits and reports | Present; provenance/evidence packaging needs R6 |
| 15 — Reproducible delivery | Compose overrides, migrations up/down and extensive component documentation | Not closed: R2, R5 and R6 |

## Limits and interpretation

The two-BET overspending case exists in the PostgreSQL integration suite; the
three-process suite additionally races three BETs. These are distinct proofs.
The new fifty-request case distributes requests across three API DNS names;
a caller barrier aligns launch but cannot force a particular SQL interleaving.

The accepted plan suggested one executable with role selection. This repository
uses separate service, workers and consumer entrypoints with shared modules.
That is a documented design variation, not a violation of the challenge.

The load reports describe offered workloads, not maximum system capacity. SQS
terminal observation includes polling delay. The observed many-wallet SQS p99
was approximately 6.55 seconds; the supplied summaries do not identify its cause.
No 100% completion percentage or guaranteed evaluator score follows from this review.
