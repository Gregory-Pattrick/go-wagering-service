# Go Wagering Service

Distributed financial processing in Go with Uber Fx, PostgreSQL, Keycloak and
SQS. HTTP and the SQS consumer share financial rules and persistent idempotency.
Wallet updates, immutable ledger entries, a double-entry journal and integration
events commit atomically. Separate workers publish the outbox and resume pending
references. OpenTelemetry, Prometheus/Grafana and HTTP/SQS load suites are included.

See [Architecture](ARCHITECTURE.md) for decisions and
[Delivery review](docs/DELIVERY-REVIEW.md) for open verification gates.
Implementation presence does not imply that every delivery gate has passed.

## Prerequisites

- Docker Engine or Docker Desktop with Linux containers and Docker Compose.
- Windows PowerShell for the supplied orchestration scripts.
- Go matching `go.mod` (1.27.1) for host commands; Docker supplies Go for container tests.
- Free loopback ports: 5432, 4566, 8080, 8081, 9090–9093, 3000 and 3200.

Run commands from the repository root. The script-block form below also works
when PowerShell blocks direct `.ps1` execution. Execute complete blocks, not
individual lines from inside their try/finally statements.

## Start the complete local stack

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action start
```

This is the primary local startup command, equivalent to the required Compose
workflow with all relevant override files/profiles. It builds the application
and the signature-validating broker, gracefully stops existing application
processes and the broker, then starts dependencies and applies migrations before
the API, workers and consumer. It enables PostgreSQL/SQS readiness, metrics,
tracing and dashboards. The final PASS requires all four readiness endpoints.

The project name is `go-wagering-service`. Existing named volumes are retained.
Starting again briefly interrupts the local application; it does not reset its
database or queues. A bare `docker compose up` selects only the base file and
is not the complete delivery topology.

| Component | Local address |
| --- | --- |
| API | http://127.0.0.1:8080 |
| Public liveness / readiness | `/health/live` / `/health/ready` on the API |
| Keycloak | http://localhost:8081 |
| Grafana dashboard | http://127.0.0.1:3000/d/wagering-operations |
| Prometheus | http://127.0.0.1:9093 |
| Tempo | http://127.0.0.1:3200 |
| API / workers / consumer operations | Loopback ports 9090 / 9091 / 9092 |

Grafana uses `admin` / `grafana_local`; Keycloak administration uses
`admin` / `admin_local`. These are public local fixtures. All published ports
are bound to loopback. The operations endpoints are unauthenticated on the
private container network; business-port `/metrics` requires the internal token.

## Run the authenticated smoke tests

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action smoke
```

This creates synthetic local wallet/transaction records and checks HTTP BET/WIN,
original-result replay, reconciliation, SQS processing, duplicate deliveries,
business rejection and actual DLQ redrive. It is not a read-only health check.

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:8080/health/live"
Invoke-RestMethod -Uri "http://127.0.0.1:8080/health/ready"
```

Both return `status: ok` when healthy. Readiness includes PostgreSQL and SQS;
liveness remains independent of dependencies.

## Status and graceful stop

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action status
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action stop
```

Stop preserves containers and named volumes. Do not use `down -v` unless you
intend to delete local data. PostgreSQL bootstrap scripts run only on an empty
data directory. Changing bootstrap passwords does not rotate existing accounts.

## Configuration and local identities

The Go application reads process environment variables, not `.env` files.
Compose supplies container addresses and credentials explicitly. Compose's own
`.env` substitution is separate from application configuration loading.

| Settings | Purpose |
| --- | --- |
| `HTTP_ADDR`, `LOG_LEVEL`, `DATABASE_URL` | HTTP listener, log threshold and application database connection |
| `OIDC_ISSUER`, `OIDC_AUDIENCE`, `OIDC_JWKS_URL` | Trusted issuer, audience and key endpoint |
| `OIDC_PROVIDER_CLIENTS`, `OIDC_INTERNAL_CLIENT` | Allowlisted service identities |
| `SQS_ENDPOINT`, `AWS_REGION`, role credential-file settings | Explicit broker endpoint and dedicated runtime credentials |
| `OBSERVABILITY_ENABLED`, `OPS_ADDR`, monitor settings | Dependency checks and metrics |
| `TRACING_ENABLED`, OTLP endpoint and sampling settings | Optional tracing |

Examples: `.env.example`, `.env.auth.example`, `.env.sqs.example`,
`.env.tracing.example`. Detailed settings are in the component documents below.
Default examples are not production secrets. Production needs TLS, managed
credentials and durable IdP storage.

| Client | Local secret | Permissions |
| --- | --- | --- |
| `wallet-service` | `wallet-service-local-secret` | Wallet management and reconciliation |
| `provider-a` | `provider-a-local-secret` | Provider A submissions and transaction reads |
| `provider-b` | `provider-b-local-secret` | Provider B submissions and transaction reads |

Keycloak imports the versioned realm automatically. Clients use
`client_credentials`; the application neither stores passwords nor issues tokens.
Recreating Keycloak restores the imported realm and signing keys; obtain new
tokens afterward. PostgreSQL data is stored independently.

Example: obtain an internal token and open a wallet:

```powershell
$token = Invoke-RestMethod -Method Post -Uri "http://localhost:8081/realms/wagering/protocol/openid-connect/token" -ContentType "application/x-www-form-urlencoded" -Body @{
    grant_type = "client_credentials"
    client_id = "wallet-service"
    client_secret = "wallet-service-local-secret"
}
$headers = @{ Authorization = "Bearer $($token.access_token)" }
$body = @{ playerId = [guid]::NewGuid().ToString(); initialBalance = @{ amount = "100.00"; currency = "BRL" } } | ConvertTo-Json -Depth 4
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8080/wallets" -Headers $headers -ContentType "application/json" -Body $body
```

For all routes, status codes, pagination and idempotency rules, see
[Financial API](docs/financial-api.md) and `scripts/smoke-api.ps1`.

## Migrations and queues

Complete startup applies every missing migration through `trace-migrate`, using
the migration account. It checks checksums and serializes migrators. The name
predates unified startup; it applies financial, accounting and tracing migrations.

Standalone migration command:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile finance run --build --rm migrate
```

Down migrations are destructive. To reverse **only a disposable test database**,
start the isolated dependency and target version zero explicitly:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile financial-testing up -d --wait finance-postgres
docker compose -f compose.yaml -f compose.finance.yaml --profile financial-testing run --build --rm --no-deps -e MIGRATION_DATABASE_URL=postgres://wagering_migrator:wagering_migrator_local@finance-postgres:5432/wagering?sslmode=disable --entrypoint go finance-tests run ./cmd/migrate -target=0 -allow-down
```

Run the financial integration suite afterward to reapply and verify migrations.
Never reverse the development database just to execute tests.

Queue provisioning creates input/output FIFO queues, their DLQs, redrive policies
and dedicated IAM identities without purging existing messages. Credentials are
stored in role-specific named volumes; application processes receive read-only
mounts, never administrative credentials. See [Broker authentication](docs/broker-authentication.md)
for the custom MiniStack signature gate and its compatibility limits.

## Verification

Host unit tests, analysis and compilation:

```powershell
go test -count=1 ./...
go vet ./...
go build ./...
```

Linux race tests (Docker avoids a host C compiler requirement):

```powershell
docker build --target test -t go-wagering-service:test .
docker run --rm go-wagering-service:test
```

Real infrastructure suites use explicit build tags and isolated test resources.
Run suites sequentially; several integration suites reset `finance-postgres`.

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile financial-testing run --build --rm finance-tests
docker compose -f compose.yaml -f compose.finance.yaml -f compose.api.yaml --profile financial-testing --profile api-testing run --build --rm api-tests
```

| Verification | Script or documentation |
| --- | --- |
| Broker signatures, IAM and restart | `scripts/test-broker-security.ps1` |
| Three APIs, fifty duplicates, HTTP/SQS and SQL audit | `scripts/test-distributed.ps1` |
| Controlled SIGKILL and restart recovery | `scripts/test-recovery.ps1` |
| Tracing and collector outage | `scripts/test-tracing.ps1` |
| Dashboard queries, real traffic and outbox recovery | `scripts/test-dashboards.ps1` |
| HTTP performance | `scripts/test-performance.ps1` |
| SQS performance | `scripts/test-sqs-performance.ps1` |
| Worker / inbox integration | [Workers](docs/workers.md), [Consumer](docs/consumer.md) |
| Readiness and dependency outages | [Observability](docs/observability.md) |

Execute a listed script using the same complete script-block form as startup.
A scenario PASS is insufficient when a script still has an SQL audit or process
inspection to complete. Preserve the final result and evidence directory.

## Documentation and delivery evidence

- [Architecture](ARCHITECTURE.md), [delivery review](docs/DELIVERY-REVIEW.md).
- [Money](docs/money.md), [transactions](docs/transactions.md), [processing](docs/processing.md), [events](docs/events.md).
- [Schema](docs/financial-schema.md), [accounting](docs/accounting.md), [persistence](docs/persistence.md).
- [Authentication](docs/authentication.md), [SQS IAM](docs/sqs-iam.md), [consumer](docs/consumer.md), [workers](docs/workers.md).
- [Tracing](docs/tracing.md), [dashboards](docs/dashboards.md), [operation logs](docs/operation-logs.md).
- [HTTP load methodology](docs/PERFORMANCE.md), [SQS load methodology](docs/SQS-PERFORMANCE.md).

Generated evidence lives under ignored `test-results`. Commit only selected
sanitized reports, preserving the measured revision, local changes and machine
configuration. Earlier measurements used the original MiniStack image and do not
measure signature-gate overhead. Final clean-checkout verification and evidence
packaging remain delivery gates; do not infer success from generated test code.
