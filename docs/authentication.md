# Authentication and Authorization

## Implementation

The application uses `github.com/coreos/go-oidc/v3` to verify JWT signatures
against a trusted Keycloak JWKS endpoint. Only RS256 is accepted. Issuer,
audience and expiration validation remain enabled. Signed Keycloak access
tokens must also have `typ=Bearer`, a valid `iat`, and an effective `nbf` when
present. A maximum 30-second future skew is allowed for `iat` only.

Verified identities are checked against a configured client-to-provider
mapping. The signed `azp`, `actor_type` and `provider_id` claims must agree
with that mapping. The internal client cannot also be a provider client.
The principal is immutable outside its package and passed through context.

The HTTP server authenticates requests before routing. Only GET/HEAD for
`/health/live` and `/health/ready` bypass authentication. This does not
implement the readiness endpoint, which remains pending.

`RequireInternal` and `RequireProvider` enforce actor categories.
`Principal.AuthorizeProvider` checks provider ownership. Business handlers
and use cases must apply these policies when implemented, including before
returning transaction lookups or idempotent replays. The financial endpoints
are not implemented by this authentication change.

## Configuration

| Variable | Local host default |
| --- | --- |
| OIDC_ISSUER | http://localhost:8081/realms/wagering |
| OIDC_AUDIENCE | wagering-api |
| OIDC_JWKS_URL | http://localhost:8081/realms/wagering/protocol/openid-connect/certs |
| OIDC_PROVIDER_CLIENTS | {"provider-a":"provider-a","provider-b":"provider-b"} |
| OIDC_INTERNAL_CLIENT | wallet-service |

These defaults match the local realm. Set all values explicitly for another
identity provider environment. HTTP is accepted for local development;
production requires HTTPS and trusted endpoint configuration.

Compose changes only the JWKS network address to `http://keycloak:8080/...`.
The expected issuer remains the public issuer, including when tokens are
obtained through the internal network. Never disable issuer validation to
resolve a hostname mismatch.

The application does not load `.env` files automatically.

## Startup and Shutdown

An Fx startup hook checks that the configured JWKS endpoint responds with a
JSON document containing an RSA signing-key candidate. Actual key parsing
and signature verification are performed by the OIDC library when a token
is verified. The HTTP server starts only after the startup check succeeds.

The HTTP client has a three-second timeout and does not follow redirects.
After the HTTP server stops, the auth hook closes idle HTTP connections.
The OIDC library caches signing keys and refreshes them when required.

## Error Contract and Current Limitation

Missing, malformed, expired or unverifiable tokens receive HTTP 401 with
an `UNAUTHENTICATED` JSON error and a Bearer challenge. Verified but
unrecognized identities, or denied authorization policies, receive HTTP
403 with a `FORBIDDEN` JSON error.

Raw access tokens and verification error details are not returned or logged.

Verification fails closed if the required signing key cannot be obtained.
Cached valid keys remain usable during an IdP outage. At this stage,
runtime JWKS retrieval failures also produce HTTP 401; they are not yet
classified separately as HTTP 503. Initial JWKS unavailability prevents
startup. Short token lifetimes limit the lifetime of cached authorization;
per-request introspection and immediate revocation are not implemented.

## Verification

Unit tests use locally generated RSA signatures and an HTTP JWKS fixture:

```powershell
go test ./...
go vet ./...
go build ./...
```

The tests cover identity spoofing, wrong issuers and audiences, invalid
signatures, disallowed algorithms, expired tokens, key rotation, cached
verification during an outage, concurrent verification, HTTP headers and
actor policies.

Integration tests obtain access tokens from the real Keycloak realm:

```powershell
docker compose --profile testing run --build --rm tests go test -tags=integration -race -count=1 ./...
```

`KEYCLOAK_TOKEN_URL` is supplied by the Compose tests service. For host
integration tests, set it to:

```powershell
$env:KEYCLOAK_TOKEN_URL = "http://localhost:8081/realms/wagering/protocol/openid-connect/token"
```

Host integration tests also require `DATABASE_URL` and running PostgreSQL
and Keycloak services.

Real-IdP tests use test-only HTTP handlers to verify provider isolation and
internal-service restrictions. They do not claim to test persistence,
financial operations or replay authorization, which are still pending.

The existing HTTP lifecycle and PostgreSQL lifecycle tests replace the OIDC
adapter so those tests continue to isolate their respective responsibilities.
