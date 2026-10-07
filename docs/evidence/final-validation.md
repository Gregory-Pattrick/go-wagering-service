# Final validation results

Tested revision: 0304031843d4be778551cd307830232ed2bb2221

Validation date: 2026-10-07 (America/Sao_Paulo).

The working tree was clean before the final distributed validation and
before the fresh-checkout validation.

## Results

| Gate | Result | Evidence |
| --- | --- | --- |
| Distributed concurrency | PASS | Supplied terminal output includes three-instance overspending, fifty concurrent duplicate requests, immutable replay, compensation races and mixed HTTP/SQS processing. |
| SQL audit | PASS | Supplied output confirms inbox completion, identities, ledger, double-entry accounting and outbox drain. |
| Process recovery | PASS reported by author | Recovery script completed successfully. |
| Complete local startup | PASS reported by author | All startup checks completed successfully. |
| Tracing and collector outage | PASS reported by author | Tracing script completed successfully. |
| Operational dashboards | PASS reported by author | Dashboard script completed successfully. |
| Fresh checkout and new volumes | PASS | Exact revision verified; migrations, readiness and HTTP/SQS smoke completed successfully. |

Distributed evidence directory:
test-results/distributed-20261007-194332

Fresh-checkout Compose project:
wagering-clean-a2504f50d29a

Fresh-checkout final output:

    PASS: fresh checkout, new volumes, migrations, readiness, HTTP and SQS.
    Validated revision: 0304031843d4be778551cd307830232ed2bb2221
    Validated project: wagering-clean-a2504f50d29a

## Scope

Broker signature/IAM checks, Go checks, API integration and operation-log
inspection passed earlier in the same delivery sequence. Their individual
tested revisions were not recorded in the supplied outputs, so they are
not relabeled as independent runs of the revision above.

Historical HTTP/SQS performance reports remain separate. They predate the
signature gate and do not measure the final revision's performance.

This documentation commit records results for the tested revision above;
it does not change application behavior.
