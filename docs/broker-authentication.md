# Local broker signature authentication

## Decision

Unmodified MiniStack 1.5.21 accepts a known access key with an incorrect secret.
`AUTH=true` enforces IAM policy decisions but does not prove possession of the
secret. The original failing `deploy/sqs/verify.py` check is retained unchanged.

`Dockerfile.broker` derives from that pinned image and places an ASGI signature
gate in front of the original MiniStack application, in the same process and
on the same listener. There is no second unprotected upstream listener. IAM
policy enforcement and actual SQS processing remain MiniStack responsibilities;
financial processing remains Go. This is a project-specific emulator adaptation,
not a feature supplied by the unmodified upstream image or a production gateway.

The gate reuses Botocore's SigV4 canonicalization and signing implementation to
recompute the signature, comparing it in constant time. It resolves secrets from
MiniStack's current IAM state. It neither copies credentials into a second store
nor caches revoked keys. Unexpected resolver errors fail closed with HTTP 503.

## Deliberately limited protocol

- Header-signed SigV4 POST requests for SQS JSON and IAM Query APIs.
- Configured region (us-east-1 in this project), one configured account, and
  long-lived IAM keys or the explicitly configured administrator key.
- Host, content type, signing date and SQS operation are covered by the signature.
- The complete body is hashed. `UNSIGNED-PAYLOAD` is rejected.
- Signing timestamps must be within five minutes of server time. This is not
  general replay prevention: financial idempotency remains mandatory.
- Duplicate headers, unsigned routing metadata, presigned query URLs, compressed
  bodies, temporary session credentials, unexpected paths/hosts and other AWS
  services are rejected. Request bodies are bounded to 2 MiB and ten seconds.
- Only GET `/_ministack/health` is public. Emulator reset/configuration/inspection
  APIs are inaccessible through this listener. Local Docker administration is
  outside this HTTP boundary; no Docker socket is mounted into the broker.

These constraints match the project's current Go and Python clients. Supporting
other SDK protocols or AWS services requires explicit compatibility work.
The container port remains 4566 and existing credential/state volumes remain in
use. No queue purge, credential rotation or database reset is required.

## Reproducible dependency boundary

The build checks SHA-256 fingerprints of the inspected upstream dispatcher,
credential resolver and IAM implementation. An unexpected source change fails
the build instead of silently running an unreviewed adapter. The image tag and
fingerprints are versioned. Unit tests execute during the image build using the
Botocore version present in that upstream image.

Base development, distributed, HTTP-performance and SQS-performance Compose files
use this image. Recovery inherits the distributed broker configuration. Python
provisioning/test containers retain the original image: they are clients, not
servers, and all network requests still pass through the signature gate.

## Verification

Run from the repository root:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-broker-security.ps1")))
```

This uses the isolated `wagering-broker-security` project, no host ports and a
separate broker volume. It builds the gate, provisions queues/policies, executes
the unchanged original verifier, verifies request signatures/tampering/inactive
keys and runs the existing IAM policy suite. It then gracefully restarts the
broker and repeats signature/policy checks against restored configuration.
Temporary IAM identities are removed by the checks. The script preserves the
isolated volume and stops its broker in finally. Only its final PASS completes
this security workflow.

Then rerun `scripts/test-distributed.ps1`. It rebuilds the Go race image and
exercises the Go SDK consumer/publisher against this broker, together with
HTTP/SQS financial idempotency and SQL accounting audits. A signature-only PASS
is insufficient to establish compatibility of all Go financial flows.

To activate the rebuilt broker in an existing development stack, stop it
gracefully and recreate it using the same complete Compose file/profile set
used to start that stack. Preserve the project name and named volumes. Do not
use `down -v`. A later unified startup command will consolidate this operation.

## Evidence and limitations

During preparation, nine Python unit tests passed. The supplied upstream Python
source was also run behind the gate over real local HTTP: provisioning, the
original verification (including wrong-secret rejection), adversarial signature
checks and IAM policy tests passed. This was not a Docker or Go SDK execution.
The Docker build, Windows workflow and Go clients still require the commands
above. Previous unmodified-emulator load numbers do not measure signature-gate
overhead; keep their original provenance and rerun if reporting this version.

No production security certification is claimed. The local defaults remain
public fixtures, traffic is local HTTP, and the emulator's durability and IAM
coverage limitations still apply. A real AWS deployment uses AWS authentication,
TLS and managed IAM credentials, not this development image.
