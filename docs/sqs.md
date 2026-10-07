# Local SQS Infrastructure

> Broker authentication update: the project now includes a custom signature gate
> in `Dockerfile.broker`. Statements below about missing SigV4 validation describe
> the unmodified upstream MiniStack image. See [broker authentication](broker-authentication.md)
> for the supported protocol, verification commands and pending runtime checks.


## Scope

MiniStack 1.5.21 emulates SQS behind the custom signature gate. The Go adapters
use the AWS SDK with an explicit local endpoint.

| Source FIFO queue | FIFO dead-letter queue |
| --- | --- |
| wager-transactions.fifo | wager-transactions-dlq.fifo |
| wager-events.fifo | wager-events-dlq.fifo |

Source queues retain messages for four days; DLQs retain them for fourteen
days. Visibility is thirty seconds and long polling is ten seconds.
Source queues use a redrive maximum receive count of five, and each DLQ
restricts redrive to its corresponding source queue.

Content-based deduplication is disabled. Senders must provide explicit
MessageDeduplicationId and MessageGroupId values. Financial idempotency
must be implemented in PostgreSQL and must not depend on FIFO deduplication.

## Startup and Provisioning

The Compose `sqs-init` service runs `deploy/sqs/bootstrap.py` after MiniStack
becomes healthy. The app container waits for successful provisioning.
Existing queues are reconciled without deletion or message purging.

```powershell
docker compose up -d --wait --wait-timeout 120 ministack
docker compose run --rm sqs-init
docker compose run --rm sqs-init
```

The second provisioning run demonstrates that existing resources can be
reused. It does not create duplicate queues.

Python and boto3 run inside the pinned emulator image. No Python or AWS CLI
installation is required on the host. They are infrastructure tooling;
the application and financial processing remain implemented in Go.

The host endpoint is `http://127.0.0.1:4566`; the Compose endpoint and
returned queue URLs use `http://ministack:4566`. Run the supplied verification
commands inside Compose so the queue hostname is resolvable.

## Verification

```powershell
docker compose run --rm sqs-init /scripts/verify.py
```

The script checks queue attributes, redrive configuration and credential
rejection, including an incorrect secret. The unmodified upstream image fails
the wrong-secret case; the custom signature gate is intended to close that gap.
Run test-broker-security.ps1 and retain its actual results before closing R1.

A temporary FIFO queue exercises send, transport deduplication, visibility,
actual redelivery and deletion. Business queue messages are never consumed
or purged. The temporary queue is removed in a finally block.

This infrastructure test does not alone prove financial idempotency or worker
recovery. IAM, consumer, distributed and recovery suites verify those behaviors
and actual DLQ movement separately.

## Authentication and Authorization Status

MiniStack starts with `AUTH=true`. Its administrative credentials are
provided only to the broker and provisioning service. The defaults are
public local-development examples; see `.env.sqs.example`.

The Go application does not receive administrative credentials.
Dedicated producer, consumer and outbox-publisher identities and
least-privilege policies are provisioned automatically.
See [SQS IAM Policies](sqs-iam.md) for verification and limitations.
Runtime provisioning writes dedicated role credentials to mounted volumes.
Signature rejection and least-privilege policy isolation are tested separately.

The shared input queue will accept messages only from a trusted internal
producer. Provider clients must not receive direct write credentials to
that shared queue: a providerId in a JSON payload alone is not authenticated
provider identity. Domain validation remains mandatory in the consumer.

The emulator port is bound to host loopback. Its local administrative and
inspection APIs are development tooling, not production security boundaries.

## Persistence and Limitations

The named `ministack_data` volume stores emulator state. `PERSIST_STATE=1`
enables state saving on orderly shutdown and restoration on startup.

Verify a graceful restart without rerunning provisioning:

```powershell
docker compose stop ministack
docker compose up -d --wait --wait-timeout 120 ministack
docker compose run --rm sqs-init /scripts/verify.py
```

The verification reads the existing business queues before creating its
isolated smoke-test queue. This checks queue configuration persistence,
not message durability during an abrupt broker kill.

Do not assume AWS-equivalent durability for emulator process crashes.
The recovery suites kill application workers while leaving
the broker and PostgreSQL running. Graceful full-stack restarts preserve
volumes. `docker compose down -v` deliberately deletes persistent data.

## Application Status

The Go SQS adapter, transactional inbox, outbox publisher and shared financial
handler are implemented. Start them with the complete command in README.md.
