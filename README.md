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