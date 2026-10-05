## Current Status

HTTP application composed with Uber Fx, environment configuration,
structured JSON logging and graceful shutdown.

The public liveness endpoint is available at GET /health/live.

Tests cover configuration validation, HTTP liveness, listener shutdown
and startup failure when the configured port is already in use.

Financial operations and external integrations are not yet implemented.

## Local Execution

```powershell
go run ./cmd/service
```

Press Ctrl+C to stop the application.

## Verification

```powershell
go test ./...
go vet ./...
go build ./...
```

## Configuration

Configuration is read from process environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| HTTP_ADDR | 127.0.0.1:8080 | HTTP listener address |
| LOG_LEVEL | INFO | DEBUG, INFO, WARN or ERROR; case-insensitive |

Invalid configuration prevents application startup.
Defaults apply only when a variable is absent.

The `.env.example` file documents the available settings.
The application does not automatically load `.env` files.

PowerShell example:

```powershell
$env:HTTP_ADDR = "127.0.0.1:9090"
$env:LOG_LEVEL = "DEBUG"

go run ./cmd/service
```

## Liveness

With the application running:

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:8080/health/live"
```

Expected response:

```json
{"status":"ok"}
```

This endpoint checks application liveness only.
Dependency readiness will be implemented alongside PostgreSQL and SQS.

## Docker

Prerequisites:

- Docker Engine or Docker Desktop with Linux containers
- Docker Compose

Build and start the application:

```powershell
docker compose up --build -d
```

Check container status and logs:

```powershell
docker compose ps
docker compose logs --tail=30 app
```

Check liveness:

```powershell
Invoke-RestMethod -Uri "http://127.0.0.1:8080/health/live"
```

Stop the application gracefully:

```powershell
docker compose stop app
```

Remove the stopped application container and Compose network:

```powershell
docker compose down
```

The application listens on `0.0.0.0:8080` inside the container.
The published port is bound to `127.0.0.1:8080` on the host.

The runtime image runs as a non-root user and contains no shell.
Its Go version matches the version declared in `go.mod`.

Compose currently runs the application and PostgreSQL.
The Go application is not connected to PostgreSQL yet.
Keycloak and local SQS provisioning will be added next.

## Linux Tests with the Race Detector

Build the test image:

```powershell
docker build --target test -t go-wagering-service:test .
```

Run tests without reusing cached test results:

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
POSTGRES_ADMIN_PASSWORD. Migration and application passwords are
defined in deploy/postgres/001-bootstrap.sql.

The application and migration accounts are not superusers.
The application account cannot create tables.
Table privileges will be granted explicitly by migrations.

### Startup

```powershell
docker compose up --build -d --wait --wait-timeout 120
```

### Database Verification

```powershell
docker compose exec postgres psql -U postgres -d wagering -c "SELECT current_database(), version();"
```

### Persistence

The postgres_data volume survives container removal with
docker compose down.

Do not add -v unless you intentionally want to delete the local database.

Bootstrap scripts run only when the data directory is empty.
Changing bootstrap SQL or initialization passwords does not update
an existing database automatically.

Subsequent schema changes must use versioned migrations.