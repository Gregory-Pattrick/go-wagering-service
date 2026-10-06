# SQS IAM Policies

## Identities

Provisioning creates three IAM users under `/wagering/` and attaches one
inline policy named `wagering-queue-access` to each user.

| User | Resource | Allowed actions |
| --- | --- | --- |
| wagering-producer | wager-transactions.fifo | GetQueueUrl, GetQueueAttributes, SendMessage |
| wagering-consumer | wager-transactions.fifo | GetQueueUrl, GetQueueAttributes, ReceiveMessage, DeleteMessage, ChangeMessageVisibility |
| wagering-outbox-publisher | wager-events.fifo | GetQueueUrl, GetQueueAttributes, SendMessage |

Policies use explicit queue ARNs and action names. No wildcard action or
resource is granted. Unlisted actions are denied implicitly. In particular,
workers cannot create, delete, purge or configure queues or access DLQs.

The producer represents a trusted internal integration, not a game provider.
Provider clients do not receive direct credentials for the shared input
queue. The consumer must still validate the envelope and domain invariants.

The bootstrap checks the user path before modifying an existing identity.
Unexpected extra policies or group memberships cause provisioning to fail
rather than silently accepting broader permissions.

## Provisioning

```powershell
docker compose run --rm sqs-init
```

The command provisions queues first, then creates or updates IAM users and
policies. Repeating it preserves the identities and reconciles the policies.
It does not issue persistent application access keys in this stage. Runtime
credential provisioning and Go SDK configuration remain pending.

Administrative credentials remain confined to infrastructure provisioning.
They must not be passed to the Go application as a shortcut.

## Verification

```powershell
docker compose run --rm sqs-init /scripts/verify_iam.py
```

The test first checks that the three provisioned users have the exact
expected policies, with no extra inline policies, managed policies or group
memberships.

It then creates disposable queues and users using the same policy templates
with disposable resource ARNs. Real signed SDK requests verify permitted
operations and HTTP 403 AccessDenied responses. Only test resources are
mutated, even if an expected denial unexpectedly succeeds.

Allowed cases include producer send, consumer receive, visibility changes
and deletion, and output publication. Denied cases include cross-queue
access, producers receiving or deleting messages, consumers sending,
publishers receiving, and every identity purging, deleting or configuring
its own queue.

Disposable access keys remain in process memory. They are neither printed
nor written into Git. The test attempts to remove all its temporary keys,
policies, users and queues before exiting. An abrupt test-container kill
can leave temporary resources; names include `wager-iam-` and a unique run
suffix. Do not remove unrelated resources when cleaning up.

These tests exercise policy templates on the real emulator and inspect the
actual provisioned policies. They do not yet exercise a Go worker with its
own runtime credentials or prove financial correctness.

## Emulator Authentication Limitation

MiniStack 1.5.21 with AUTH=true evaluates IAM permissions but does not
validate general SigV4 signatures. A valid access key can therefore be
impersonated with an incorrect secret in this local emulator.

Successful IAM policy tests demonstrate authorization behavior only. They
do not resolve or hide the separate incorrect-secret test failure in
`deploy/sqs/verify.py`. Full broker authentication must be verified against
an environment implementing signature validation before claiming that
security requirement is fully demonstrated.

## Integration Status

The Go SQS adapter, runtime credentials, inbox consumer, outbox publisher,
DLQ processing and financial authorization remain pending. No provider ID
from an untrusted message body may become an authenticated identity.
