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