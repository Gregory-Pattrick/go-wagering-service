# Delivery checklist and result record

This file contains pending gates, not an assertion that they passed. Fill it
with observed results after applying the broker-authentication and delivery-
readiness blocks. Do not replace an error with a manually printed PASS.

## Execution order

1. Finish the broker-authentication APPLY.md, then delivery-readiness APPLY.md.
2. Apply and commit this documentation-only evidence block. Confirm a clean
   working tree and push before collecting final revision evidence.
3. Run the final regression gates below sequentially. Reuse a result only when
   its tested code revision and scope are known and still apply.
4. Follow FINAL-VALIDATION.md for the fresh clone and new-volume smoke test.
5. Record sanitized outcomes and close review findings with evidence. Commit
   that result record separately; identify the preceding tested code revision.

## Result record

Keep PENDING until the corresponding command has actually completed. Record the
revision printed before execution, date/time with timezone, exact command and
sanitized result or evidence directory. If code changes to fix a failure, record
that new revision and rerun the affected gates.

| Gate | Status | Tested revision | Actual evidence |
| --- | --- | --- | --- |
| Unit tests, vet and build | PENDING | — | — |
| Financial API / PostgreSQL integration and race detector | PENDING | — | — |
| Broker signatures, IAM denial checks and graceful restart | PENDING | — | — |
| Complete startup, migrations and readiness | PENDING | — | — |
| HTTP/SQS smoke and operation identifier logs | PENDING | — | — |
| Distributed concurrency, fifty duplicates and SQL audit | PENDING | — | — |
| Four controlled process-crash recovery windows | PENDING | — | — |
| Tracing and collector outage | PENDING | — | — |
| Dashboards, live traffic, backlog and recovery | PENDING | — | — |
| Fresh clone with new volumes | PENDING | — | — |
| Final-revision HTTP/SQS performance, if claimed | PENDING | — | Historical reports are explicitly separate |
| Repository access, final push and clean status | PENDING | — | — |

Previous user-reported PASS results remain historical evidence. This table is
for final delivery validation; it does not retroactively invalidate earlier runs.

## Commands for the remaining regression gates

Run from the normal repository root after both implementation blocks pass:

```powershell
Set-Location "D:\Desenvolvimento\Projetos\go-wagering-service"
git rev-parse HEAD
git status --short
Get-Date -Format o
```

If there are uncommitted changes, inspect and commit them before recording final
revision measurements. Run each command below separately, waiting for its final
result. Do not paste the entire list and continue after a failure.

Distributed correctness:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-distributed.ps1")))
```

Controlled application process recovery:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-recovery.ps1")))
```

Restore the complete local topology before observability checks:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action start
```

Tracing and collector outage:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-tracing.ps1")))
```

Dashboard queries and publisher recovery:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-dashboards.ps1")))
```

To obtain measurements for the final code revision, run separately with other
load/test suites stopped. These create isolated load topologies but share host
CPU and RAM, so record other running services in the evidence:

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-performance.ps1")))
```

```powershell
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\test-sqs-performance.ps1")))
```

Do not treat a producer/generator PASS as a complete result: wait for the SQL
audits and final script result. Retain the full local evidence directory and
commit only reviewed summaries and metadata. Historical reports remain under
[docs/evidence](evidence/README.md).

## Final handoff

Use FINAL-VALIDATION.md for the clean clone; it temporarily stops the normal
stack to free host ports without deleting its volumes. Afterward:

- Update DELIVERY-REVIEW.md with actual evidence for R1–R6; document open items.
- Check the README startup instructions against what succeeded in the clone.
- Record the tested code revision and the later documentation-only evidence
  commit separately. Do not claim that an untested code change was covered.
- Confirm `git status` is clean and `git push origin main` succeeded.
- Confirm the evaluator has access to the repository and the final branch.

Suggested evidence commit: `docs: record final validation results and review closure`.
A checked list does not guarantee a score or correctness in every possible execution.
