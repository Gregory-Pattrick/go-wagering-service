# Final delivery validation

This is a checklist, not a pre-filled PASS report. Run gates sequentially and
stop at the first error. Do not commit tokens, runtime credentials or raw logs.

## 1. Validate the amended checkout

Apply and validate the broker-authentication block first, then the delivery-
readiness block. Its APPLY.md contains the exact unit, integration, security,
startup, smoke and commit commands. Existing performance reports predate the
signature gate and must retain their original revision and dirty-file metadata.

Run distributed and recovery suites on the amended checkout. Run tracing and
dashboard tests after the complete local stack starts. These tests serve
separate purposes; one PASS does not substitute for another. Re-run load tests
if presenting performance numbers as measurements of the amended revision.

## 2. Preserve evidence and provenance

For each selected report, record:

- Tested Git revision, whether it was dirty, date and exact command.
- Host/Docker resources and configured topology.
- Actual scenario counts, assertions, failures and final outcome.
- For load: offered/completed rate, dropped work, p50/p95/p99, replays/rejections,
  SQL audits, backlog and drain/recovery measurements.

Copy only reviewed, sanitized summaries into `docs/evidence/`. Keep original
reports intact elsewhere. Never replace the old revision with the current hash.
A suitable commit is `docs: record validated delivery evidence`.

## 3. Test a fresh checkout and new volumes

Only after pushing the validated implementation, open PowerShell and stop the
normal development stack to release its host ports. This preserves its volumes:

```powershell
Set-Location "D:\Desenvolvimento\Projetos\go-wagering-service"
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action stop
```

Copy and run this complete block. It clones into a new folder, checks that the
checkout is clean, and uses a unique Compose project so its named volumes are
new. The finally block stops its containers while retaining failure evidence.
It does not stop or delete unrelated projects.

```powershell
& {
    $ErrorActionPreference = "Stop"
    $suffix = [guid]::NewGuid().ToString("N").Substring(0, 12)
    $cleanProject = "wagering-clean-$suffix"
    $cleanFolder = "D:\Desenvolvimento\Projetos\$cleanProject"
    git clone "https://github.com/Gregory-Pattrick/go-wagering-service.git" $cleanFolder
    if ($LASTEXITCODE -ne 0) { throw "Clone failed" }
    Set-Location $cleanFolder
    $revision = git rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw "Cannot read revision" }
    $changes = git status --porcelain
    if ($LASTEXITCODE -ne 0 -or $changes) { throw "Checkout is not clean" }
    Write-Output "Tested revision: $revision"
    Write-Output "Compose project: $cleanProject"
    $runner = [scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))
    try {
        & $runner -Action start -ProjectName $cleanProject
        & $runner -Action smoke -ProjectName $cleanProject
        $changes = git status --porcelain
        if ($LASTEXITCODE -ne 0 -or $changes) { throw "Smoke changed tracked/untracked repository files" }
        Write-Output "PASS: fresh checkout, new volumes, migrations, readiness, HTTP and SQS."
        Write-Output "Validated revision: $revision"
    } finally {
        & $runner -Action stop -ProjectName $cleanProject
    }
}
```

Record the actual output and exact project name. Do not print a replacement PASS
manually if any assertion fails. To inspect the stopped clone later, use its
folder and the printed project name with `local-stack.ps1 -Action status
-ProjectName <name>`. Do not delete volumes to disguise a failed run.

Return to the normal repository and restore its existing stack:

```powershell
Set-Location "D:\Desenvolvimento\Projetos\go-wagering-service"
& ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\local-stack.ps1"))) -Action start
```

## 4. Close the review

Update DELIVERY-REVIEW.md only with observed evidence. R1 needs signature and
Go regression results; R2 needs the fresh startup above; R3 needs HTTP/SQS
identifier examples; R4 has author-reported fifty-request results; R5 needs a
final documentation check; R6 needs provenance-preserving evidence and the clean
checkout result. Commit the evidence and review update separately from code.

Confirm the final push, clean Git status and evaluator repository access. The
last documentation-only commit may refer to the exact preceding tested code
revision; state that distinction explicitly. No checklist guarantees an evaluator
score or correctness for every possible execution.
