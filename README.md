## Current Status

Minimal application composed with Uber Fx, structured JSON logging,
and startup and shutdown hooks.

Includes a test covering application composition and the initial lifecycle.
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
| HTTP_ADDR | 127.0.0.1:8080 | HTTP listener address, reserved for the upcoming server |
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

The HTTP server is not implemented yet; no port is opened at this stage.