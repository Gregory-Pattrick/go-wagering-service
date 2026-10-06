# Go Wagering Service

Go backend for a wagering service, currently under development.

## Current Status

HTTP application composed with Uber Fx, environment configuration,
structured JSON logging, PostgreSQL connectivity and graceful shutdown.

The public liveness endpoint is available at `GET /health/live`.

The application connects to PostgreSQL through `pgxpool` during startup.
The connection pool is initialized before the HTTP server starts and
closed after the HTTP server stops.

Tests cover configuration validation, HTTP liveness, listener shutdown
and startup failure when the configured port is already in use.
Integration tests cover PostgreSQL connectivity, application database
role and connection pool shutdown.

Keycloak is provisioned with local service identities and client credentials
token issuance. The Go HTTP server verifies access tokens before routing,
except for public health checks.

Actor authorization policies and provider ownership checks are covered by
unit tests and real-Keycloak integration tests. Financial endpoints and idempotent replays now apply these policies
before accessing provider-scoped records.

Financial domain rules, versioned schema, accounting constraints and SQL
repositories are implemented. Financial HTTP routes and persistent replay are implemented. SQS runtime
integration remains pending. See [Financial Persistence](docs/persistence.md) for validation.

## Prerequisites

- Go matching the version declared in `go.mod` for execution on the host
- Docker Engine or Docker Desktop with Linux containers
- Docker Compose

The commands below use PowerShell.

## Local Execution

Start PostgreSQL:

```powershell
docker compose up -d --wait --wait-timeout 240 postgres keycloak
```

Stop the containerized application if it is using port 8080:

```powershell
docker compose stop app
```

Configure the host application and start it:

```powershell
$env:HTTP_ADDR = "127.0.0.1:8080"
$env:LOG_LEVEL = "INFO"
$env:DATABASE_URL = "postgres://wagering_app:wagering_app_local@127.0.0.1:5432/wagering?sslmode=disable"

go run ./cmd/service
```

Press Ctrl+C to stop the application.

The application does not automatically load `.env` files.

## Configuration

Configuration is read from process environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| HTTP_ADDR | 127.0.0.1:8080 | HTTP listener address |
| LOG_LEVEL | INFO | DEBUG, INFO, WARN or ERROR; case-insensitive |
| DATABASE_URL | None; required | PostgreSQL connection URL |

Invalid configuration prevents application startup.
Defaults apply only when a variable is absent.

The `.env.example` file documents the available settings.

Compose explicitly provides `DATABASE_URL` using the `postgres` service
hostname. When running Go on the host, use `127.0.0.1` instead.

`POSTGRES_ADMIN_PASSWORD` configures the local PostgreSQL administrator
password through Compose and defaults to `postgres_local`.
It is not an application configuration variable.

## Liveness

With the application running on port 8080:

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:8080/health/live"
```

Expected JSON response:

```json
{"status":"ok"}
```

PowerShell displays the response as an object.

This endpoint checks application liveness only. It does not query
PostgreSQL or report dependency readiness.

A dependency readiness endpoint covering PostgreSQL and SQS is planned.

## Docker

Build and start the application and PostgreSQL:

```powershell
docker compose up --build -d --wait --wait-timeout 240
```

Check container status and application logs:

```powershell
docker compose ps
docker compose logs --tail=40 app
```

Check liveness:

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:8080/health/live"
```

Stop the application gracefully:

```powershell
docker compose stop app
```

Start it again:

```powershell
docker compose start app
```

Stop and remove the Compose service containers and network:

```powershell
docker compose down
```

The application listens on `0.0.0.0:8080` inside the container.
The published port is bound to `127.0.0.1:8080` on the host.

The runtime image runs as a non-root user and contains no shell.
The build stage uses the Go version declared in `go.mod`.

Compose starts the application after the PostgreSQL health check succeeds.
The application then verifies its own database connection before starting
the HTTP listener.

The `tests` service uses the `testing` profile and does not start during
the default Compose startup.

Compose also provisions Keycloak with an automatically imported realm.
Local SQS queues are provisioned automatically through MiniStack.

## Verification

Run unit and HTTP lifecycle tests, static analysis and compilation:

```powershell
go test ./...
go vet ./...
go build ./...
```

These commands do not require a running PostgreSQL instance.
Database integration tests use the separate `integration` build tag.

## Linux Tests with the Race Detector

Build the test image:

```powershell
docker build --target test -t go-wagering-service:test .
```

Run tests with the race detector and without cached test results:

```powershell
docker run --rm go-wagering-service:test
```

Run static analysis:

```powershell
docker run --rm go-wagering-service:test go vet ./...
```

Rebuild the test image after changing source files.
Building the runtime image does not automatically run the test stage.

The race detector checks Go memory access during executed tests.
Distributed financial correctness requires additional integration tests.

## Local PostgreSQL

PostgreSQL runs with a persistent named volume and a health check.

| Setting | Value |
| --- | --- |
| Image | postgres:17.11-bookworm |
| Host address | 127.0.0.1:5432 |
| Compose network address | postgres:5432 |
| Database | wagering |
| Application schema | wagering |

### Local Development Accounts

| Account | Purpose | Local password |
| --- | --- | --- |
| postgres | Bootstrap and administration | postgres_local |
| wagering_migrator | Schema migrations | wagering_migrator_local |
| wagering_app | Application queries | wagering_app_local |

These credentials are for local development only.

The administrator password can be configured with
`POSTGRES_ADMIN_PASSWORD`. Migration and application passwords are
defined in `deploy/postgres/001-bootstrap.sql`.

The application and migration accounts are not superusers.
The application account cannot create tables.
Table privileges will be granted explicitly by migrations.

### Database Verification

With PostgreSQL running:

```powershell
docker compose exec postgres psql -U postgres -d wagering -c "SELECT current_database(), version();"
```

### Persistence

The `postgres_data` volume survives container removal with
`docker compose down`.

Do not add `-v` unless you intentionally want to delete the local database.

Bootstrap scripts run only when the data directory is empty.
Changing bootstrap SQL or initialization passwords does not update
an existing database automatically.

Subsequent schema changes must use versioned migrations.

## PostgreSQL Connection

The application connects to PostgreSQL through `pgxpool` using
`DATABASE_URL`.

The database initialization hook uses a five-second timeout.
Startup fails if the initial database connection cannot be established.

The pool is initialized before the HTTP server starts and closed after
the HTTP server stops.

Each application instance uses a pool with a maximum of 10 connections.
The Compose application uses the `wagering_app` database account.

### Integration Tests

Run integration tests against the real PostgreSQL container with the
race detector enabled:

```powershell
docker compose --profile testing run --build --rm tests go test -tags=integration -race -count=1 ./...
```

This command builds the test image and starts PostgreSQL as a dependency.

Integration tests verify database connectivity, the `wagering_app` role,
its non-superuser status and connection pool shutdown.

Financial migrations and their isolated integration suite are documented in
[Financial Persistence](docs/persistence.md).

## Local Identity Provider

Keycloak runs at http://localhost:8081 and imports the `wagering` realm
from `deploy/keycloak/wagering-realm.json`.

Start and verify the identity provider:

```powershell
docker compose up -d --wait --wait-timeout 240 keycloak
docker compose ps keycloak

Invoke-RestMethod -Uri "http://localhost:8081/realms/wagering/.well-known/openid-configuration"
```

### Local Service Identities

| Client ID | Local client secret | Actor type | Provider ID |
| --- | --- | --- | --- |
| provider-a | provider-a-local-secret | provider | provider-a |
| provider-b | provider-b-local-secret | provider | provider-b |
| wallet-service | wallet-service-local-secret | internal | Not applicable |

These credentials are public development examples, not production secrets.

Clients use `client_credentials`. Interactive login, implicit flow and
password grants are disabled for these clients.

Tokens expire after five minutes and include the `wagering-api` audience.
Provider identities are assigned by Keycloak through fixed token claims.

Request a provider token:

```powershell
$tokenResponse = Invoke-RestMethod `
    -Method Post `
    -Uri "http://localhost:8081/realms/wagering/protocol/openid-connect/token" `
    -ContentType "application/x-www-form-urlencoded" `
    -Body @{
        grant_type = "client_credentials"
        client_id = "provider-a"
        client_secret = "provider-a-local-secret"
    }

$providerToken = $tokenResponse.access_token
```

The admin console is available at http://localhost:8081/admin/.
The bootstrap username is `admin`. Its password defaults to `admin_local`
and can be configured through `KEYCLOAK_ADMIN_PASSWORD`.

### Development Lifecycle

Keycloak uses its embedded development database without a persistent
volume. Recreating the Keycloak container discards administrative changes
and signing keys, then imports the versioned realm configuration again.

After editing the realm JSON, recreate only Keycloak:

```powershell
docker compose up -d --force-recreate --wait --wait-timeout 240 keycloak
```

Obtain new tokens after recreation. The PostgreSQL data volume is unaffected.

A normal container restart preserves the existing realm. Startup import
does not overwrite a realm that already exists.

This setup uses HTTP and development mode for local execution.
Production deployment requires TLS, managed secrets and a production
identity-provider database.

### Implementation Status

Token issuance, JWT signature verification and HTTP authentication are
implemented. Liveness remains public.

Financial endpoints and their resource-level authorization are still
pending.

See [Authentication](docs/authentication.md) for configuration, policies,
test commands and current limitations. Host configuration examples are
available in `.env.auth.example`; the application does not load this file
automatically.

## SQS Implementation Status

The local environment includes input and output FIFO queues, their
dead-letter queues and redrive configuration.

Provisioning is repeatable and preserves existing messages.
Functional checks cover queue configuration, delivery, visibility,
redelivery, deletion and transport deduplication.

Dedicated producer, consumer and outbox-publisher IAM identities and
least-privilege policies are provisioned automatically.

See [SQS IAM Policies](docs/sqs-iam.md) for permissions and verification.

Runtime credentials for the Go application, the Go SQS adapter,
financial consumers, inbox and outbox processing are not implemented yet.

Administrative credentials are local provisioning fixtures and are not
provided to the Go application.

See [Local SQS Infrastructure](docs/sqs.md) for setup and verification.

### Local Emulator Limitation

With AUTH=true, MiniStack enables IAM policy evaluation but does not
validate general SigV4 signatures.

The unknown-access-key test passes. The incorrect-secret test fails on
version 1.5.21 and remains a documented security limitation.

Functional SQS checks pass independently of that verification.
Broker security verification is not complete.

## Money Value Object

Monetary values use exact `int64` minor units and explicit BRL or USD
currencies. External amounts must be nonnegative decimal strings with
exactly two fractional digits.

The immutable value object validates input, rejects currency mismatches
and detects arithmetic overflow. No monetary operation uses floating point.

See [Money Value Object](docs/money.md) for the input contract, internal
signed values and verification details.

## Wallet Domain

The wallet aggregate supports creation, rehydration and exact credit
and debit operations. It validates currency compatibility, prevents
negative balances and checks monetary and version overflow.

Wallets start at version 1. Each successful balance change increments
the version once. Rejected operations leave the original state unchanged.

See [Wallet Aggregate](docs/wallet.md) for domain rules and the planned
SQL transaction boundary.

SQL persistence, authenticated wallet routes and wallet row locking are
implemented. Three-process concurrency verification remains pending.

## Transaction Domain

Financial transactions validate operation kinds, amount policies and
reference requirements. Internal OPENING transactions have a separate
constructor and do not require external provider metadata.

The domain enforces lifecycle transitions and terminal-state protection.
Financial result snapshots preserve the original balance and wallet
version for future persisted replays.

Canonical business payloads are hashed with SHA-256. The shared application
service now implements database-backed idempotency and financial processing.

See [Transaction Identity and Lifecycle](docs/transactions.md).

## Wallet Ledger Domain

Immutable ledger entries record each movement's direction, amount,
balance before and balance after.

Construction and rehydration enforce exact balance equations,
compatible currencies, positive movement amounts and nonnegative
balances. Invalid values and arithmetic overflow are rejected.

See [Immutable Wallet Ledger](docs/ledger.md).

Database constraints, immutable ledger protection and atomic persistence
are included in the financial persistence block.

## Financial Processing Rules

The domain evaluator connects transactions, wallets and ledger entries
for BET, WIN, LOSS, REFUND and ROLLBACK.

It validates reference eligibility, full reversal amounts and financial
context. Missing or pending references leave operations waiting without
changing the wallet.

Successful movements produce a new wallet state, a processed transaction
and one ledger entry. LOSS preserves the balance and version without
creating an entry. Business rejections preserve the wallet.

See [External Financial Processing Rules](docs/processing.md).

The HTTP API and SQL adapter persist financial decisions atomically.
Durable retry workers and distributed verification remain pending.

## Financial Domain Events

Typed, immutable events describe processed transactions, business
rejections, reference waits and wallet balance changes.

Envelopes include event identity, correlation, optional causation,
UTC occurrence time, schema version and typed financial data.
Internal OPENING events omit external provider metadata.

Events preserve the original decision snapshot. Outbox persistence is
implemented; broker publication remains pending.

See [Typed Financial Events](docs/events.md).

## Financial Database Block

See [Financial Schema](docs/financial-schema.md),
[Double-Entry Accounting](docs/accounting.md) and
[Financial Persistence](docs/persistence.md).

Apply the migrations:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile finance run --build --rm migrate
```

Run the isolated database suite with the race detector:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile financial-testing run --build --rm finance-tests
```

The test database is separate from the normal development database. The tests
include destructive migration reversal only in that isolated database.

## Financial HTTP API

Wallet creation, wallet/ledger queries, wagering submissions, transaction queries
and reconciliation are available through authenticated routes. Financial replay
returns the original stored result without applying another movement.

See [Financial HTTP API](docs/financial-api.md) for exact routes, request limits,
status codes, idempotency policy, pagination and remaining work.

Run the real PostgreSQL and Keycloak API suite separately from finance-tests:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml -f compose.api.yaml --profile financial-testing --profile api-testing run --build --rm api-tests
```

Rebuild the normal service and run the PowerShell smoke script:

```powershell
docker compose up --build -d --wait --wait-timeout 180 app
.\scripts\smoke-api.ps1
```

Pending references and outbox events are durable, but their background workers
are not yet implemented. Broker readiness and full observability remain pending.

## Durable Workers

Reference recovery and outbox publication run in a separate Fx process.
See [Durable reference and outbox workers](docs/workers.md) for startup,
configuration, least-privilege publisher credentials and recovery tests.

## SQS Input Consumer

The separate `cmd/consumer` process handles input FIFO messages with a
transactional inbox and the same financial use case as HTTP. See
[SQS consumer and transactional inbox](docs/consumer.md) for its trust boundary,
acknowledgment rules, broker redrive, local execution and real SQS smoke test.
