# Operational dashboards

Apply the tracing block first, including its instrumentation helper and pinned
Go dependencies. This block adds no Go dependencies or database migrations.

From the repository root, run `scripts/test-dashboards.ps1`. If Windows blocks
script execution, open the file in VS Code and paste its entire contents into
PowerShell at the repository root. Do not paste only the function body.

The script combines the base, finance, workers, consumer, telemetry, tracing and
observability Compose files. It enables the workers, consumer, tracing and
observability profiles. Prometheus and its checks are opt-in. The dashboard uses
the Grafana instance introduced by the tracing block.

| Component | Local URL | Credentials |
| --- | --- | --- |
| Grafana dashboard | http://127.0.0.1:3000/d/wagering-operations | admin / grafana_local |
| Prometheus | http://127.0.0.1:9093 | No authentication; loopback only |
| Tempo API | http://127.0.0.1:3200 | No authentication; loopback only |

`GRAFANA_ADMIN_PASSWORD` overrides the initial local Grafana password. Grafana
persists accounts in its volume: changing this variable does not reset an
existing account's password. The check must use the account's actual password.

## What the verification does

1. Validate Compose and run `promtool check config` using the pinned image.
2. Build/start the application, workers, consumer, collector, Grafana and Prometheus.
3. Stop the workers process, temporarily pausing both publication and reference resolution.
4. Create three new wallets and execute real bets, wins, original-result replays,
   identity conflicts and insufficient-funds rejections; reconcile the wallets.
5. Wait until Prometheus observes positive outbox backlog and age.
6. Restart workers in a PowerShell `finally` block, including after test failure.
7. Verify all three scrape targets, outbox drain, provisioned dashboard,
   Grafana's datasource proxy and every dashboard PromQL expression.
8. Require finite data for transaction latency counters, replays, rejections
   and queue depths. Print each panel's number of finite series.

The script leaves the stack running for inspection and creates real local test
records. It does not purge queues, delete database rows or remove volumes.
If the terminal is forcibly closed during the pause, rerun the complete script
or start workers with the same Compose file/profile combination.

Open the dashboard immediately after the test. Its default range is 15 minutes;
latency and throughput use two-minute windows, so short smoke traffic ages out.
Watch the backlog rise and fall in the history. This is a functional demo, not a
load benchmark. The automated check validates provisioning and data access;
visually inspect panel layout in Grafana as well.

## Metric interpretation

- HTTP p50/p95/p99 are histogram estimates, including database and lock time.
  They are not dedicated lock-wait measurements.
- Outcomes include labeled idempotent replays; throughput is not a count of
  unique financial movements.
- Retry counters measure observed reschedules/transport retries. The conflict
  panel counts surfaced errors, not every internally retried SQL conflict.
- Database snapshots and queue depths select the API scrape job to avoid
  counting the same shared database/queues multiple times. If adding API
  replicas, use a deliberate single observer or deduplicate snapshots.
- Queue and DLQ depths are approximate. DLQ depth is not a redrive counter.
- Reconciliation mismatches count executed reconciliation checks, not an
  automatic full-ledger audit.
- An absent series or NaN is displayed as missing data. It is never replaced
  with a fabricated zero. Rare retry/reference/consumer-duration panels may
  have no series until those operations execute; prior consumer and reference
  integration scenarios provide that traffic. This smoke does not inject DLQ
  failures or fabricate reconciliation mismatches.
- Scrape availability and dependency availability are separate. The sample-age
  panel identifies stale checks even when an HTTP scrape still succeeds.

Prometheus scrapes private operations endpoints at app:9090, workers:9090 and
consumer:9090 inside Compose. Host ports 9091/9092 are not container target ports.
Prometheus retains seven days in `prometheus_data`; other named volumes retain
Grafana configuration and traces. Host bindings stay on 127.0.0.1.

Dashboard JSON and provisioning are version controlled. UI edits are disabled;
edit the JSON and commit it. Restart Grafana after changing datasource/provider
configuration. Dashboard files are refreshed by its provisioning provider.

The known MiniStack incorrect-secret validation limitation remains unchanged.
A passing dashboard check does not validate SigV4 security or replace financial,
concurrency, recovery, integration and race-detector tests.

## References

- https://prometheus.io/docs/prometheus/latest/configuration/configuration/
- https://prometheus.io/docs/prometheus/latest/command-line/promtool/
- https://grafana.com/docs/grafana/latest/administration/provisioning/
- https://prometheus.io/download/?trk=direct
