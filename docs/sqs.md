# Local SQS Infrastructure

> Broker authentication update: the project now includes a custom signature gate
> in `Dockerfile.broker`. Statements below about missing SigV4 validation describe
> the unmodified upstream MiniStack image. See [broker authentication](broker-authentication.md)
> for the supported protocol, verification commands and pending runtime checks.


## Scope

MiniStack 1.5.21 emulates the AWS SQS API locally. The Go application will use
the AWS SDK and explicit endpoint configuration in a later step.

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

The script checks queue attributes and redrive configuration, then tests
credential rejection. MiniStack 1.5.21 rejects unknown access keys with
AUTH=true, but accepts a known access key signed with an incorrect secret.

The incorrect-secret test therefore fails and remains a documented
security verification gap. AUTH=true enables IAM policy evaluation;
it does not provide general SigV4 signature authentication.

A temporary FIFO queue exercises send, transport deduplication, visibility,
actual redelivery and deletion. Business queue messages are never consumed
or purged. The temporary queue is removed in a finally block.

This does not yet prove financial idempotency, worker crash recovery,
least-privilege IAM policies or actual DLQ movement after exhausted retries.
Those require the subsequent policy and consumer integration stages.

## Authentication and Authorization Status

MiniStack starts with `AUTH=true`. Its administrative credentials are
provided only to the broker and provisioning service. The defaults are
public local-development examples; see `.env.sqs.example`.

The Go application does not receive administrative credentials.
Dedicated producer, consumer and outbox-publisher identities and
least-privilege policies are provisioned automatically.
See [SQS IAM Policies](sqs-iam.md) for verification and limitations.
Runtime credential provisioning for the Go application remains pending.
Successful denial tests for bad credentials do not demonstrate policy
isolation between those future identities.

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
Upcoming worker-crash tests should kill application workers while leaving
the broker and PostgreSQL running. Graceful full-stack restarts preserve
volumes. `docker compose down -v` deliberately deletes persistent data.

## Application Status

No SQS Go adapter, consumer, inbox, outbox publisher or financial handler is
implemented by this infrastructure change.
