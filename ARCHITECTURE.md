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

## Transaction Lifecycle and Identity

External transactions start in PENDING and may transition to
PENDING_REFERENCE, PROCESSED, REJECTED or FAILED. Pending-reference
transactions may transition to a terminal state. Terminal states cannot
be changed.

Retryable infrastructure failures must not become permanent FAILED
outcomes. Unknown commit outcomes require retrying the same identity
and checking persisted state.

The business payload hash excludes internal transaction IDs,
idempotency keys and transport metadata. HTTP and SQS will share the
same domain input and canonical hashing implementation.

Processed results retain the balance and wallet version observed at
the original decision. Replay must use that snapshot.

This domain layer does not yet enforce persistent idempotency,
reference eligibility or atomic financial effects. Those guarantees
require application rules and PostgreSQL transactions.

See [Transaction Identity and Lifecycle](docs/transactions.md) for
the complete state machine, hash contract and failure codes.

## Wallet Ledger Invariants

Each wallet ledger entry represents one positive financial movement.
Its balance equation is validated using exact Money arithmetic.

Entries expose no mutation methods. Corrections require new
compensating transactions and entries. Rehydration validates stored
data without applying another movement.

The processing service must ensure that only eligible financial
operations produce entries. LOSS, rejected operations and zero-balance
wallet creation produce no ledger entry.

Planned PostgreSQL enforcement includes uniqueness of
(wallet_id, transaction_id), immutable records and atomic persistence
with wallet balances, transaction state and outbox records.

The double-entry journal is implemented in migration 002, preserving one
wallet ledger entry per financial movement.

## Financial Decisions and Compensation Policy

The domain evaluator performs no I/O and returns a decision for the
application service to persist atomically.

References must match provider, external identity, player, wallet,
currency and round. REFUND and ROLLBACK require the original full amount.
WIN may reference a BET with a different amount.

A BET permits one successful direct compensation: REFUND or ROLLBACK.
Rolling back a REFUND does not reopen the original BET's compensation
right. The evaluator uses compensation history supplied by the caller;
database locking and constraints must enforce this policy concurrently.

Reference lookup must be scoped by provider and external transaction ID.
The wallet, reference ledger and compensation history must be loaded
consistently inside the financial SQL transaction.

The application must persist the decision together with the required
ledger, inbox and outbox records before publishing any events.

See [External Financial Processing Rules](docs/processing.md).

## Event Snapshots and Outbox Contract

Event constructors define the event type and schema version.
All financial events use wallet ID as aggregate ID.

Successful movements produce transaction-processed and balance-changed
events. LOSS produces only transaction-processed. Repeated reference
waits produce no additional logical event, and terminal replays must
not invoke event construction.

The application must persist event IDs and serialized snapshots in
the same SQL transaction as the financial decision. The future outbox
publisher must reuse those stored bytes and IDs on every retry.

Event construction alone does not provide persistent deduplication
or delivery guarantees.

See [Typed Financial Events](docs/events.md).

## Implemented SQL Transaction Boundary

The financial adapter uses pgx v5 with explicit SQL. Money is represented by
BIGINT minor units and currency, with no float conversion. Store.Within owns
one READ COMMITTED transaction shared by all Unit repository methods.

Wallet writers use SELECT FOR UPDATE and version-checked updates. Immutable
ledger entries form a versioned balance chain. Deferred database constraints
require matching transaction state, ledger, result snapshots and outbox data.
Every movement also requires a balanced, immutable two-posting journal.
Clearing accounts have no shared mutable balance.

Pending transactions require durable work rows. Terminal transitions remove
that work. Inbox completion and outbox insertion can share the same financial
commit. Migration and application database roles remain separate.

The adapter classifies retryable SQL errors without automatically repeating
callbacks. Unknown commit outcomes require lookup using the original identity.
The application layer must supply bounded retries, authorization and transport
idempotency handling. Background workers and HTTP financial routes remain pending.

See [Financial Persistence](docs/persistence.md) for commands, implemented
integration tests, timestamp precision and the current validation status.
