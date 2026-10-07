# Go Wagering Service

Distributed financial processing in Go with Uber Fx, PostgreSQL, Keycloak and
SQS. HTTP and the SQS consumer share financial rules and persistent idempotency.
Wallet updates, immutable ledger entries, a double-entry journal and integration
events commit atomically. Separate workers publish the outbox and resume pending
references. OpenTelemetry, Prometheus/Grafana and HTTP/SQS load suites are included.

This repository is a backend service. Grafana provides operational dashboards;
there is no player-facing web interface at the API root.

The Windows/Docker workflow was validated from a fresh clone with new volumes
at revision `0304031843d4be778551cd307830232ed2bb2221`. See
[recorded validation](docs/evidence/final-validation.md),
[delivery review](docs/DELIVERY-REVIEW.md) and [architecture](ARCHITECTURE.md).
Later documentation commits do not change that tested code revision.

## Contents

- [Set up a new Windows machine](#set-up-a-new-windows-machine)
- [Clone the repository](#clone-the-repository)
- [Start the complete local stack](#start-the-complete-local-stack)
- [Run the authenticated smoke tests](#run-the-authenticated-smoke-tests)
- [Status and graceful stop](#status-and-graceful-stop)
- [Configuration and local identities](#configuration-and-local-identities)
- [Optional: run the API directly with Go](#optional-run-the-api-directly-with-go)
- [Migrations and queues](#migrations-and-queues)
- [Verification](#verification)
- [Project structure](#project-structure)
- [Troubleshooting](#troubleshooting)
- [Documentation and delivery evidence](#documentation-and-delivery-evidence)

## Set up a new Windows machine

The primary walkthrough uses Windows PowerShell and Docker Desktop with Linux
containers. All commands after installation run in a normal PowerShell terminal.
Internet access is required for cloning, image downloads and Go modules during
image builds. The first build is slower because dependencies are not cached.

### 1. Install Git

Install [Git for Windows](https://git-scm.com/install/windows), including its
command-line tools. Close and reopen PowerShell after installation, then check:

```powershell
git --version
```

GitHub authentication is not required to clone this public repository. Git author
configuration is only needed if you intend to create commits.

### 2. Install WSL and Docker Desktop

Follow the official [Docker Desktop Windows installation guide](https://docs.docker.com/desktop/setup/install/windows-install/)
for the supported Windows version, hardware virtualization and WSL requirements.
For the WSL backend, install WSL from an administrator PowerShell if it is absent:

```powershell
wsl --install
```

Restart Windows if requested. If WSL is already installed, check/update it:

```powershell
wsl --version
wsl --update
```

Install Docker Desktop using its WSL 2 backend, then open Docker Desktop and wait
for its engine to start. Use Linux containers. Docker Desktop supplies Docker
Compose; a separate Compose installation is not required for this walkthrough.

Open a new normal PowerShell terminal and verify:

```powershell
docker --version
docker compose version
docker info --format '{{.OSType}}'
docker run --rm hello-world
```

The operating-system result must be `linux`, and `hello-world` must run
successfully. Resolve engine/WSL errors before proceeding with the project.
The default Windows terminal is sufficient; an editor such as VS Code is optional.

### 3. Choose Docker execution or host development

**For complete local execution, Git, Docker Desktop and PowerShell are enough.**
Go, PostgreSQL, Keycloak, Python, boto3 and the observability tools run in
containers. No AWS account, cloud SQS queue or host database installation is
needed. The local broker is MiniStack with the repository's signature gate.

Install [Go](https://go.dev/doc/install) only if you want to run host Go commands
or develop the API outside its container. Match `go.mod` and the Docker build
version: **Go 1.27.1**. Check with `go version` after reopening your terminal.

The recorded environment used Docker 29.8.2, Compose 5.5.1 and WSL 3.0.1.0 on
Windows x64. These are tested versions, not a claim about minimum requirements.
The full stack runs several services; ensure Docker has sufficient memory and
free disk space for image builds. The historical load environment allocated
six CPUs and about 16 GiB RAM to Docker; this is not a measured minimum.

Keep these loopback ports free:
`5432`, `4566`, `8080`, `8081`, `9090`, `9091`, `9092`, `9093`, `3000`, `3200`.

## Clone the repository

Choose a writable development directory. This example works without a D: drive:

```powershell
$projectsDirectory = Join-Path $env:USERPROFILE "source"
New-Item -ItemType Directory -Force -Path $projectsDirectory | Out-Null
Set-Location $projectsDirectory

git clone https://github.com/Gregory-Pattrick/go-wagering-service.git
Set-Location .\go-wagering-service

git status
git rev-parse HEAD
```

If cloning fails, stop and resolve it before running subsequent commands. If you
already have a checkout, enter that folder instead of cloning over it. Record the
revision when collecting test evidence.

**Run all remaining commands from the repository root**, where `compose.yaml`
and `README.md` are located. Do not run them from `C:\Windows\System32`.
No downloaded patches are needed: the repository contains the implementation.

### First-run configuration

**No `.env` file is required for the default local setup.** Compose provides local
fixture credentials and dependency addresses. The `.env*.example` files document
settings; copying all of them or installing dependencies manually is unnecessary.
For the first run, keep the supplied defaults. Existing environment variables or
an existing `.env` may override Compose substitutions.

The script-block commands below run the checked-in PowerShell script without
changing the machine's execution policy. Review the script before execution.
Paste complete commands, including their parentheses; do not paste the `PS ...>`
prompt. Wait for each step to complete before starting the next.

## Start the complete local stack

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action start
```

This is the primary local startup command, equivalent to the required Compose
workflow with all relevant override files/profiles. It builds the application
and the signature-validating broker, gracefully stops existing application
processes and the broker, then starts dependencies and applies migrations before
the API, workers and consumer. It enables PostgreSQL/SQS readiness, metrics,
tracing and dashboards. The final PASS requires all four readiness endpoints:

```text
PASS: migrations, API, workers, consumer and dependency readiness.
```

Successful initializer/migration containers may show `Exited (0)`; they are
one-shot jobs. The API, workers, consumer and long-running dependencies must
remain running. A nonzero exit or a failed readiness check is not a successful
startup. Do not continue to smoke tests after a startup error.

The project name is `go-wagering-service`, independent of the checkout folder.
Existing named volumes are retained.
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
Wait for the entire script, including DLQ checks, to complete:

```text
PASS: authenticated HTTP and SQS smoke tests.
```

A successful HTTP check alone is not a successful full smoke test.

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

To start again later, open PowerShell, return to this checkout and repeat
`local-stack.ps1` with `-Action start` using the script-block command above.
There is no need to clone again or reinitialize the database.

Inspect logs from the default project:

```powershell
docker compose -p go-wagering-service logs --tail=80 app postgres keycloak ministack
docker compose -p go-wagering-service -f compose.yaml -f compose.workers.yaml --profile workers logs --tail=80 workers
docker compose -p go-wagering-service -f compose.yaml -f compose.consumer.yaml --profile consumer logs --tail=80 consumer
```

These shorter file sets are for reading logs from existing containers. Use the
complete startup script when starting or recreating the application.

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

## Optional: run the API directly with Go

Use this only for API development after the complete Docker startup has succeeded.
PostgreSQL, Keycloak, SQS, workers and the consumer remain in Docker. Migrations
must already have been applied by the complete startup command.

Stop only the containerized API to free port 8080:

```powershell
docker compose -p go-wagering-service stop app
```

In a separate PowerShell window at the repository root, configure the host API:

```powershell
$env:HTTP_ADDR = "127.0.0.1:8080"
$env:LOG_LEVEL = "INFO"
$env:DATABASE_URL = "postgres://wagering_app:wagering_app_local@127.0.0.1:5432/wagering?sslmode=disable"
$env:OIDC_ISSUER = "http://localhost:8081/realms/wagering"
$env:OIDC_AUDIENCE = "wagering-api"
$env:OIDC_JWKS_URL = "http://localhost:8081/realms/wagering/protocol/openid-connect/certs"
$env:OIDC_PROVIDER_CLIENTS = '{"provider-a":"provider-a","provider-b":"provider-b"}'
$env:OIDC_INTERNAL_CLIENT = "wallet-service"
$env:OBSERVABILITY_ENABLED = "false"
$env:TRACING_ENABLED = "false"

go run ./cmd/service
```

This development mode disables the host API's metrics/readiness module and trace
export. Check `/health/live`; it is not equivalent to complete-stack readiness.
The containerized SQS smoke uses `http://app:8080` and must not be run with the
API container stopped. The host HTTP smoke can run from another terminal:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\smoke-api.ps1")))
```

Press Ctrl+C to stop the host API, close that dedicated terminal to discard its
environment overrides, and restore the full Docker stack with `-Action start`.
The container workflow is the delivery-validated path; this host alternative is
for development and was not separately recorded in the final smoke evidence.

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

## Project structure

| Path | Responsibility |
| --- | --- |
| `cmd/service`, `cmd/consumer`, `cmd/workers` | HTTP, SQS ingestion and background-process entrypoints |
| `cmd/migrate` | Versioned database migrations |
| `cmd/load-sqs`, `cmd/crashprobe` | Load generation and controlled recovery tests |
| `internal/domain` | Exact money, wallet, transaction, processing, ledger and event rules |
| `internal/application` | Financial use cases and delivery contracts |
| `internal/adapters` | HTTP, PostgreSQL, SQS and OIDC integrations |
| `internal/bootstrap`, `internal/config` | Fx composition, lifecycle and configuration |
| `internal/workers`, `internal/observability`, `internal/operationlog` | Background loops, metrics/tracing and operation identifiers |
| `deploy` | Local infrastructure configuration and scenario tooling |
| `scripts` | PowerShell startup and verification workflows |
| `docs` | Contracts, architecture details, test methodology and evidence |

Go tests live beside the corresponding source in `*_test.go` files. Financial
rules are shared by HTTP and SQS; the domain does not import transport or database
libraries. See ARCHITECTURE.md for transaction boundaries and failure handling.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `git` or `docker` is not recognized | Finish installation, reopen PowerShell and repeat version checks. |
| Docker named-pipe error or daemon unavailable | Start Docker Desktop; verify WSL and Linux containers with `docker info` and `hello-world` before retrying. |
| PowerShell says script execution is disabled | Use the complete script-block commands shown above; no global policy change is needed. Organizational controls may still require administrator support. |
| `Run this script from the repository root` | Enter the cloned folder containing `compose.observability.yaml`. |
| A port is already allocated | Stop the previous project or host process using that port. Different Compose project names isolate volumes, not published host ports. |
| `Found orphan containers (...prometheus...)` during tracing tests | The tracing-only file set omits Prometheus. Do not use `--remove-orphans` to remove a service still needed by the full stack. |
| Broker build fails its source/signature checks | Keep the checks enabled, use the committed Dockerfile and pinned image, and inspect the actual build error. Do not bypass authentication checks. |
| API returns 401 | Obtain a fresh token using the supplied local client; check issuer/audience and whether Keycloak was recreated. |
| API returns 403 | Check actor type and provider ownership; wallet endpoints require the internal client. |
| API root `/` returns an error | There is no frontend at `/`. Use `/health/live`, authenticated API routes or Grafana. |
| Startup/readiness or smoke fails | Inspect status and service logs. Do not treat intermediate PASS lines as a complete successful run. |
| Credentials changed but the database still rejects login | Bootstrap SQL runs only with an empty data directory; changing examples does not rotate existing database users. |

For Docker engine problems, use the official installation guide linked above.
For a failing application test, retain its error and evidence directory. Do not
purge queues or delete named volumes to disguise a failure. Avoid publishing
bearer tokens, runtime credential files or complete application logs.

## Documentation and delivery evidence

- [Architecture](ARCHITECTURE.md), [delivery review](docs/DELIVERY-REVIEW.md).
- [Final validation results](docs/evidence/final-validation.md), [delivery checklist](docs/DELIVERY-CHECKLIST.md), [fresh-checkout procedure](docs/FINAL-VALIDATION.md).
- [Historical performance evidence and provenance](docs/evidence/README.md).
- [Money](docs/money.md), [transactions](docs/transactions.md), [processing](docs/processing.md), [events](docs/events.md).
- [Schema](docs/financial-schema.md), [accounting](docs/accounting.md), [persistence](docs/persistence.md).
- [Authentication](docs/authentication.md), [SQS IAM](docs/sqs-iam.md), [consumer](docs/consumer.md), [workers](docs/workers.md).
- [Tracing](docs/tracing.md), [dashboards](docs/dashboards.md), [operation logs](docs/operation-logs.md).
- [HTTP load methodology](docs/PERFORMANCE.md), [SQS load methodology](docs/SQS-PERFORMANCE.md).

Generated evidence lives under ignored `test-results`. Commit only selected
sanitized reports, preserving the measured revision, local changes and machine
configuration. Earlier measurements used the original MiniStack image and do not
measure signature-gate overhead. Final distributed checks and fresh-checkout
validation are recorded for revision `0304031843d4be778551cd307830232ed2bb2221`.
Recovery, tracing and dashboard results are identified as author-reported where
only completion was supplied. No final-revision performance measurement or
unconditional correctness guarantee is claimed.
