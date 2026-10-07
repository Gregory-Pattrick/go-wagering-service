# Operation identifier logs

The `financial operation completed` JSON record links committed operations across
HTTP, SQS and the outbox. It uses a fixed field allowlist; identifiers containing
control characters or excessive lengths are omitted. The financial domain does
not depend on logging, and JSON serialization of log fields cannot change money.

| Field | Meaning |
| --- | --- |
| transport | http, sqs or outbox |
| correlationId | HTTP correlation header/generated ID, or deterministic SQS envelope correlation |
| messageId | Input envelope identity, only when available from SQS |
| transactionId | Persisted financial transaction identity |
| walletId | Wallet identity in the operation |
| providerId | Verified HTTP provider or trusted SQS routing identity |
| eventId | Persisted outgoing event identity |
| causationId | Outbox envelope causation, when present; not assumed to always be a message ID |
| status | OPENED, financial status or PUBLISHED |
| failureCode | Stable business rejection code, when present |
| idempotentReplay | Whether a financial submission returned an existing result |

Wallet opening logs the returned wallet ID; its response does not expose an
opening transaction ID. The balance-change event has no provider field, so the
publisher log cannot invent one. Other outgoing transaction events carry their
external provider metadata. HTTP/SQS representations of one business operation
can have different correlation IDs; transactionId joins them after processing.

HTTP completion logs occur after successful use-case return, before writing the
response. SQS completion logs occur after durable inbox/financial completion,
before receipt deletion. An acknowledgment failure can therefore lead to another
replay log without another movement. Outbox completion logs occur after durable
publication acknowledgment. A process can crash between commit and logging;
logs are diagnostic evidence, not the financial source of truth.

No amount, balance, player name, bearer token, idempotency key, receipt handle or
complete financial payload is included by this record. Original error/lifecycle
logs and tracing identifiers remain available separately. Unauthorized requests
do not produce a financial-completion record.

## Verification

```powershell
go test -count=1 ./internal/operationlog ./internal/adapters/http ./internal/adapters/sqs ./internal/workers ./internal/bootstrap
```

Then run the real API suite and the complete local startup/smoke commands from
README. Inspect local JSON records without uploading full logs:

```powershell
docker compose logs --since=10m app | Select-String 'financial operation completed'
docker compose -f compose.yaml -f compose.workers.yaml --profile workers logs --since=10m workers | Select-String 'financial operation completed'
docker compose -f compose.yaml -f compose.consumer.yaml --profile consumer logs --since=10m consumer | Select-String 'financial operation completed'
```

The shorter file sets above only select existing container logs; use the full
local-stack script for startup/recreation. Expect HTTP and SQS transaction IDs,
wallet/provider/correlation IDs, SQS envelope message IDs and published event IDs.
The same transaction ID should join an SQS submission with its HTTP replay.
