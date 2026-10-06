# Financial Schema

Migration 001 creates wallets, transactions, wallet ledger entries, inbox,
outbox and durable transaction work. Amounts and versions use BIGINT; currency
is explicit. Monetary equations cast to NUMERIC only to check integer arithmetic
without intermediate BIGINT overflow. No monetary float is used.

The schema enforces unique player/currency wallets, both external idempotency
identities, one opening per wallet and one successful direct compensation per
referenced transaction. Completed reference rows remain immutable, so rolling
back a refund does not reopen compensation eligibility for its original BET.

Wallet changes require matching ledger entries with consecutive wallet versions.
Positive opening starts at version 1 with a credit from zero; zero opening has
no financial records. Transactions, results, references and event snapshots are
cross-checked by deferred constraint triggers at commit. Pending transactions
must have durable work; terminal transactions must not retain pending work.

The application role cannot change ledger entries, terminal transactions, event
payloads or wallet identities. UPDATE/DELETE/TRUNCATE protection combines explicit
privileges and triggers. The migration owner and administrator remain trusted:
no schema can prevent its owner from intentionally dropping its protections.

Migrations use the existing wagering_migrator account. Version and checksum
history is stored in wagering.schema_migrations. Published migrations must not
be edited after deployment. The migration runner and commands are described in
[persistence.md](persistence.md).
