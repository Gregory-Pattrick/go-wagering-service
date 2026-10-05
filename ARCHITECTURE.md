# Architecture

This document is updated as implementation progresses.

## Authentication and Authorization

### Identity Provider

Keycloak is the external OAuth 2.0 / OpenID Connect identity provider.
Service clients authenticate through the client credentials grant.

The application does not store end-user passwords or issue its own tokens.

The local realm is versioned and imported automatically by Docker Compose.
Two provider clients allow cross-provider isolation tests. A separate
internal client represents the wallet management service.

### Identity Model

Access tokens target the `wagering-api` audience.

Keycloak assigns fixed claims to each service client:

- `actor_type=provider` and `provider_id` for provider clients.
- `actor_type=internal` for the wallet service.

Provider identity must come from a verified access token. Request bodies
and URL parameters cannot establish or override the authenticated provider.

### API Enforcement

The HTTP server authenticates requests before routing, except for public
GET/HEAD health checks. The OIDC adapter verifies RS256 signatures against
the trusted Keycloak JWKS, issuer, audience, expiration and identity claims.
Client identities are checked against an explicit configuration allowlist.

The configured issuer is `http://localhost:8081/realms/wagering`.
Container networking may require a separate trusted internal JWKS URL;
this must not disable issuer validation.

Provider clients will be restricted to submitting and reading their own
transactions. Authorization must also run before returning idempotent
replays. Wallet endpoints will require the internal service identity.

Unrecognized identities and unsupported actor types are denied.
The actor-policy middleware and provider-ownership guard are tested, but
their use in financial handlers and replay use cases is still pending.

### Local Environment

Keycloak runs in development mode with an ephemeral embedded database.
Recreating its container restores the versioned realm and rotates signing
keys. PostgreSQL financial data is stored independently in a named volume.

Local client secrets are reproducible development fixtures.
Production requires TLS, managed secrets and durable IdP storage.

### Current Status

Realm provisioning, service token issuance, Go token verification and
HTTP authentication are implemented. Real-Keycloak tests exercise actor
policies and provider isolation through test-only handlers. Financial
endpoint authorization and replay isolation remain pending with those
endpoints and use cases.

See [Authentication](docs/authentication.md) for configuration, lifecycle,
tests and limitations, including runtime JWKS failures currently returning
HTTP 401 instead of a separately classified HTTP 503.
Broker authentication and authorization will be documented with SQS setup.