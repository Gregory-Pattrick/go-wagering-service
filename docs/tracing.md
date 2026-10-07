# Distributed tracing

Tracing is optional and disabled unless `TRACING_ENABLED=true`. It is composed
with the existing metrics feature by `InstrumentAPI`, `InstrumentWorkers` and
`InstrumentConsumer`. Each type has a single Fx decorator, so enabling tracing
and metrics together does not register conflicting decorators.

## Trace paths

- HTTP server span -> financial transaction -> PostgreSQL transaction and
  persist-decision span -> durable outbox context -> outbox publish span -> SQS
  message attributes.
- SQS trace attributes -> consumer processing span -> shared inbox/financial
  transaction -> PostgreSQL persistence -> durable outbox -> publication.
- Pending-reference outbox context -> each reference attempt -> PostgreSQL
  transaction -> terminal event metadata -> later publication.

Read operations, wallet-lock acquisition, outbox claim/confirmation/retry and
SQS deletion/visibility changes also have spans. SQL spans measure repository
operations and transaction duration, not every individual SQL statement.
Lock spans include the wait and query work; they are not a pure lock-wait metric.
Deletion/visibility spans use the incoming transport context and are siblings
of the processing span. Polling claims without a known event have separate
traces; they are not falsely attached to an unrelated wallet trace.

## Durable metadata and idempotency

Migration `003_tracing` creates `wagering.outbox_trace_context`, keyed by event ID.
The application inserts traceparent/tracestate inside the same SQL transaction
as the outbox event. Rollback removes both. The metadata is immutable; financial
replays do not overwrite the origin with the replay request's trace.

Business payloads, money, immutable event bodies and financial/inbox hashes are
unchanged. SQS uses separate message attributes for traceparent and tracestate.
Event ID remains the FIFO deduplication ID; wallet ID remains the FIFO group.
Only W3C trace context is propagated. Baggage, HTTP authorization headers,
SQL text/parameters, tokens, connection strings and financial request bodies
are not recorded as span attributes.

Publishers restore context from SQL after claiming work. The trace metadata read
has a one-second bound; if it fails, delivery still proceeds with a new trace.
Persisting new metadata is part of the financial SQL transaction, so the schema
must be migrated before tracing is enabled. Missing migrations are configuration
errors, not collector outages. The old migrations and their checksums are unchanged.

## Export and outage behavior

Application export uses an asynchronous batch processor with a 2,048-span queue,
256-span export batches, a one-second batch interval and a two-second export
timeout. The SDK exporter retries are disabled. The queue does not block the
financial request when full. Failed or overflowing application batches may be
lost; no durable trace-export guarantee is claimed.

The collector has its own bounded queue and retry budget. The application does
not depend on collector readiness, and PostgreSQL/SQS readiness excludes it.
Shutdown gives trace export at most three seconds, then logs incomplete export
without changing a completed financial result. Parent-based sampling defaults
to 100% locally; remote sampling flags are honored. Use 100% for the smoke test.

Structured logs emitted with an active context include `trace_id` and `span_id`.
SQL operation summaries are DEBUG-level; HTTP, publication, consumer and
reference summaries are INFO-level. Lifecycle messages without an active
span have no trace ID. The HTTP response includes `X-Trace-ID` for business routes;
health and metrics requests are excluded from request tracing.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| TRACING_ENABLED | false | Enable the tracing composition |
| OTEL_EXPORTER_OTLP_TRACES_ENDPOINT | http://otel-collector:4318/v1/traces | Full OTLP/HTTP trace endpoint |
| TRACING_SAMPLE_RATIO | 1 | Root trace sampling probability from 0 to 1 |
| GRAFANA_ADMIN_PASSWORD | grafana_local | Local UI fixture, used by Compose |

`.env.tracing.example` documents the settings; Go does not automatically load it.
The default collector hostname is for Compose networking. No OTLP receiver is
published to the host. A Go process running on Windows needs an explicitly
reachable collector endpoint if tracing is enabled there.

`compose.tracing.yaml` adds Collector, Tempo and Grafana, plus a migration job and
test services. Combine it with the application, finance, workers and consumer
Compose files; adding `compose.telemetry.yaml` enables the metrics feature too.
The trace profile is `tracing`. Collector availability is not an application
startup requirement. Tempo keeps local trace blocks for 24 hours.

## Visual inspection

Grafana: http://127.0.0.1:3000 (local user `admin`, password `grafana_local`, unless
overridden on first initialization). Open Explore, select Tempo, choose Trace ID
and paste an ID printed by the smoke test. The Tempo data source is provisioned
automatically. Its UID is `wagering-tempo`.

Tempo's local API is available at http://127.0.0.1:3200. Both published ports are
bound to loopback. Collector receivers remain inside the Compose network.
Operational Prometheus dashboards are provided by compose.observability.yaml; tracing provisions
the trace viewer only. Existing Grafana volumes retain their initial password.

## Validation

The package includes tests for config validation, context and log correlation,
nonblocking span completion during an exporter stall, stable SQS identity and
payload, and transactional outbox metadata/rollback against PostgreSQL.

`deploy/tracing/smoke.py healthy` queries the real Tempo API for three paths:
HTTP, SQS and reference recovery. It requires expected span names and service
names, and checks that an outbox publisher span's parent is a persisted SQL-write
span. Merely printing a trace ID is not considered success.

`scripts/test-tracing.ps1` runs that smoke, stops the collector, executes financial
operations through HTTP/SQS/reference recovery, restores the collector in a
finally block, and generates fresh traffic to verify resumed export. It does
not require lost traces from the outage to reappear. API replay with a different
trace context must retain the original transaction and balance snapshot.

Trace reports are stored in the `tracing_results` named volume. No input or
output queue is purged. The smoke adds uniquely identified local wallet fixtures
and reconciles them; it does not reset the development database. Database tests
use only the isolated `finance-postgres` service and must not run concurrently
with other suites that reset it.

Preparation included static syntax/SQL/patch checks. Compilation, Docker image
validation, tests and real traces must be verified locally before claiming this
block passed. Broker signature enforcement is provided by the separate authentication
candidate; see broker-authentication.md for its pending runtime gates.

## Pinned dependencies and primary references

- OpenTelemetry Go SDK/OTLP HTTP v1.47.0: https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
- Collector contrib 0.162.0: https://github.com/open-telemetry/opentelemetry-collector-releases/releases
- Tempo 2.9.0: https://github.com/grafana/tempo/blob/main/CHANGELOG.md
- Grafana 13.2.2: https://grafana.com/grafana/download
- OTLP export: https://opentelemetry.io/docs/languages/go/exporters/

Versions are pinned for reproducible local setup; they are not a claim that all
components are the newest available versions or a production deployment template.
