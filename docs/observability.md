# Dependency readiness and process metrics

This block is enabled with `OBSERVABILITY_ENABLED=true` in
`compose.telemetry.yaml`. With the variable absent or false, the earlier
application commands retain their previous behavior. Invalid values fail startup.
The executable entrypoints call the optional Fx telemetry composition.

## Endpoints and scope

| Process | Local operations address | Endpoints |
| --- | --- | --- |
| API | http://127.0.0.1:9090 | /health/live, /health/ready, /metrics |
| Reference/outbox workers | http://127.0.0.1:9091 | /health/live, /health/ready, /metrics |
| SQS consumer | http://127.0.0.1:9092 | /health/live, /health/ready, /metrics |

The API also exposes public `/health/ready` on its business port 8080. Its
business-port `/metrics` still requires a valid internal Keycloak token; provider
and anonymous access remain forbidden. Operational listeners expose only health
and aggregate metrics, without authentication. They default to loopback for
host execution. Compose binds their host ports to 127.0.0.1 and makes them
available inside the private Compose network for the configured Prometheus scraper.
Do not publish those operational ports on an internet-facing interface.

The initial state is not ready until a probe completes. Probes run every five
seconds with a shared three-second deadline; PostgreSQL and queue checks run in
parallel. Readiness returns 503 when any required dependency fails or the sample
is stale (older than twice the interval plus three seconds). It returns 200 after
successful recovery. Results are cached so HTTP health requests and scrapes do
not multiply database or SQS traffic. Readiness is not an instantaneous or
transactional guarantee that the next operation will succeed.

The API checks the financial PostgreSQL schema plus input/output queues and their
DLQs. Workers check PostgreSQL and their output queue; the consumer checks
PostgreSQL and its input queue. The checks use real queue metadata requests,
not TCP reachability alone. The monitor IAM identity has only GetQueueAttributes
on the four queue ARNs; API containers do not receive provisioning credentials.
Workers and consumer reuse their already restricted role keys for metadata calls.

Liveness answers whether the process can serve HTTP. A dependency outage does not
make liveness fail. PostgreSQL is still required at process startup by the existing
database lifecycle hook; outage/recovery tests stop it only after startup.

Readiness does not reject direct business requests. Orchestrators use it to route
traffic. A direct HTTP request can still commit during an SQS outage, leaving its
events in the durable outbox for later delivery. Queue publication failures do not
roll back already committed financial operations.

## Metrics

Prometheus collectors use a registry per process, without global registration.
The pinned client is `github.com/prometheus/client_golang v1.23.2`.

| Metric family | Meaning |
| --- | --- |
| wagering_financial_outcomes_total | Committed application outcomes by transport, status and replay flag |
| wagering_operation_errors_total | Bounded categories of SQL/application/worker errors |
| wagering_retries_total | Reference rescheduling, outbox retry scheduling and SQS re-deliveries/visibility changes |
| wagering_worker_steps_total | Completed worker actions: receives, deletes, reference attempts and outbox acknowledgments |
| wagering_operation_duration_seconds | Histograms for reference processing, SQS send and SQS handling |
| wagering_http_requests_total | Request count by bounded route and HTTP response class |
| wagering_http_duration_seconds | Request latency histograms by bounded route |
| wagering_dependency_up | Latest successful/failed dependency check |
| wagering_dependency_sample_timestamp_seconds | Time of the latest completed probe |
| wagering_database_state | Global PostgreSQL state, collected by API processes only |
| wagering_sqs_messages | Approximate visible/in-flight/delayed depth by fixed queue alias |
| wagering_reconciliation_mismatches_total | Discrepancies observed by the API process |
| go_* / process_* | Standard Go runtime and process collectors |

Database measures include transaction counts by status, unpublished outbox count,
oldest unpublished event age, retained outbox retry attempts, pending reference
count/age/current attempts and completed inbox rows. These are gauges describing
persisted state. Pending-reference attempts disappear from that gauge when work
completes; it must not be treated as a lifetime retry counter. Outbox age is based
on event occurrence time. Database failure produces NaN for these values rather
than misleading zeros. Queue failures likewise produce NaN depths and dependency_up=0.

The gauges describe global shared state. When multiple API processes expose the
same database snapshot, use one scrape target or aggregate with max, not sum.
Process counters reset when that process restarts; use Prometheus rate/increase.
They are operational observations, not an accounting source of truth. Commit
outcomes that remain unknown are recorded as errors, not claimed as successful.
The database remains authoritative after recovery.

`OPENED` denotes the wallet-creation use case, including zero opening. A later
reference completion is a new observed outcome, not a second business transaction.
Replays are labeled explicitly. Financial counters are incremented only after the
unit of work commits; preliminary domain decisions inside rolled-back SQL are not
counted as successful. HTTP validation failures before SQL are visible in HTTP 4xx
metrics; they are not committed financial outcomes.

DLQ depth is obtained from the broker. It is not a fabricated count of messages
"sent to DLQ" when the consumer merely retains a receipt. The preceding consumer
smoke separately observes an actual malformed message after broker redrive.

No wallet ID, player ID, provider ID, transaction ID, message ID, correlation ID,
raw URL or error string becomes a metric label. Logs carry available correlation,
event, causation, envelope and delivery IDs. Tokens, credentials and complete
financial payloads are not logged. Operation latency includes lock waiting; a
separate lock-wait measurement is not yet provided.

## Configuration

| Variable | Default / purpose |
| --- | --- |
| OBSERVABILITY_ENABLED | false when absent; true enables the Fx modules |
| OPS_ADDR | 127.0.0.1:9090; operations listener |
| PROBE_INTERVAL | 5s; accepted range 1s to 1m |
| AWS_REGION | Required signing region when enabled |
| SQS_ENDPOINT | Required broker endpoint |
| MONITOR_CREDENTIALS_FILE | Restricted role key file |
| MONITOR_QUEUES | JSON object using aliases input/output/input_dlq/output_dlq |

The Compose override supplies these values. The metadata-only API key is stored
in a dedicated read-only volume. Local key fixtures are not production workload
identity or key rotation. The upstream MiniStack wrong-secret limitation and the custom signature gate
are documented in broker-authentication.md.

## Full local stack

Use the unified startup command in README.md. It includes migrations, all three
roles, dependency readiness, metrics, tracing and dashboards. The loopback port
mappings support one instance per role; compose.distributed.yaml provides the
separate multi-instance correctness topology.

## Verification and limits

The delivered APPLY.md includes syntax/build/unit/race/database checks and an
ordered outage smoke sequence. The smoke checks all three operations listeners
and business-port readiness, stops SQS, commits a new financial operation through
the still-live API, restores SQS and checks outbox drain/reconciliation. It also
checks PostgreSQL outage/recovery and unavailable metric samples.

Dependency stop/start is explicit in PowerShell. The smoke container receives no
Docker socket and cannot stop host services. No test deletes volumes or resets the
normal database. PostgreSQL integration tests use only finance-postgres.

Prometheus, Grafana and Tempo are configured by the observability and tracing
overrides. Use the complete startup in README.md. Distributed recovery and load
suites are implemented separately; their results must identify the tested revision.
