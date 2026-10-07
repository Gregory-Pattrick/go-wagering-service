# Financial Persistence

The adapter uses pgx v5, already present in go.mod. Domain Money maps to signed
BIGINT minor units plus a currency code. Wallet and movement constraints reject
negative balances and nonpositive movements. No floating-point conversions occur.

## Unit of Work

`finance.New(pool).Within(ctx, callback)` begins one READ COMMITTED SQL transaction.
The callback receives a Unit whose repositories share that exact pgx transaction.
It must return every error. An error or panic rolls back; success commits once.
No callback may publish to a broker or send an irreversible external response.

`OpenWallet` writes zero or positive openings. Positive openings include OPENING,
ledger, journal, both postings and two events in the same transaction.
`SaveDecision` persists a processing decision, its optional wallet movement,
ledger, journal, events and pending-work lifecycle. `CompleteInbox` joins that
same transaction for the SQS consumer.

`LockWallet` uses SELECT FOR UPDATE. Every writer must acquire the wallet lock
before evaluating its balance and loading compensation history. SaveWallet also
checks the previous version. Reference reads are provider-scoped. Identity
insertion uses ON CONFLICT DO NOTHING, allowing the application to inspect both
existing identities without leaving SQL in an aborted state.

Pending work records store attempts, schedule, expiry and lease fields. Outbox
records store event identity, immutable JSONB snapshot and delivery/lease fields.
Workers claim leased work, update retry schedules and publish stored outbox
snapshots. The application service coordinates durable transport idempotency.
JSONB stores an immutable semantic snapshot, not the original JSON whitespace.

READ COMMITTED plus per-wallet locking serializes competing wallet writers;
there is no global financial lock. The migration runner alone uses a separate
advisory lock. Deadlock/serialization errors are classified by Retryable; the
adapter does not silently rerun callbacks. The application must implement bounded
whole-transaction retry. Unknown commit outcomes return ErrCommitUnknown and
require lookup/retry with the same identity, never a new operation identity.

PostgreSQL timestamps have microsecond precision. Construct financial objects
with `time.Now().UTC().Truncate(time.Microsecond)` so persisted snapshots and
event timestamps remain identical. Repository writes reject finer precision.

## Applying Migrations

From the repository root, with Docker Desktop running:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml config --quiet
docker compose -f compose.yaml -f compose.finance.yaml --profile finance run --build --rm migrate
```

The migration service starts the normal PostgreSQL dependency and uses the
migration role. It applies missing versions, verifies existing checksums and is
safe to repeat. It does not change the main Compose file or require a database
reset. Apply all three migrations before starting the current application.

Go execution on the host is also available:

```powershell
$env:MIGRATION_DATABASE_URL = "postgres://wagering_migrator:wagering_migrator_local@127.0.0.1:5432/wagering?sslmode=disable"
go run ./cmd/migrate
```

Down migrations delete financial tables. They are only for disposable databases.
The CLI requires an explicit `-allow-down` for a lower target. Do not run them
against your normal development database to execute tests.

## Isolated Integration Tests

```powershell
docker compose -f compose.yaml -f compose.finance.yaml --profile financial-testing run --build --rm finance-tests
```

This starts `finance-postgres`, a separate PostgreSQL container with no host
port and ephemeral storage. The tests refuse to reset any host other than
finance-postgres or database other than wagering. The normal postgres volume
is not touched. Tests apply migrations, exercise the database, reverse and
reapply migrations, and verify checksum protection.

Coverage includes positive/zero openings, duplicate wallets, rollback after
all financial writes, direct invalid SQL, immutable records, incomplete journals,
wrong accounting accounts, missing events, two concurrent BETs of 80.00 against
100.00, an unrelated wallet while another is locked, original replay snapshots,
provider-scoped lookups, reversals, compensation history, LOSS and durable pending
state. These tests use separate database connections within one Go process;
separate distributed and recovery suites exercise multiple processes and
application process crashes; see distributed-tests.md and recovery-tests.md.

The suite uses -race and does not reuse cached results. To remove only its
isolated test database container:

```powershell
docker compose -f compose.yaml -f compose.finance.yaml rm -s -f finance-postgres
```

No `down -v` command is required.

## Integration

HTTP and SQS use the shared financial application service. Authorization precedes
lookup and replay. Wallet state, transaction state, ledger, journal, inbox where
applicable, and outbox commit atomically. Use the unified startup in README.md;
it applies migrations before the financial processes start.
