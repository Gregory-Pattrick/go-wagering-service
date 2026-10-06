# Run from the repository root. Copy the full script into PowerShell if scripts are blocked.
$ErrorActionPreference = "Stop"
$composeArgs = @("compose", "-p", "wagering-distributed", "-f", "compose.distributed.yaml")
$processes = @("api-1", "api-2", "api-3", "publisher-1", "publisher-2", "ingress-1", "ingress-2")
$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$evidencePath = Join-Path (Get-Location).Path "test-results\distributed-$timestamp"
New-Item -ItemType Directory -Force -Path $evidencePath | Out-Null

function Invoke-DistributedCompose {
    param([string[]]$Arguments)
    & docker @composeArgs @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker Compose failed: $($Arguments -join ' ')" }
}

try {
    Invoke-DistributedCompose -Arguments @("config", "--quiet")
    Invoke-DistributedCompose -Arguments @("build", "api-1", "migrate")
    Invoke-DistributedCompose -Arguments (@("up", "-d") + $processes)
    Invoke-DistributedCompose -Arguments @("run", "--rm", "--no-deps", "distributed-test")
    Invoke-DistributedCompose -Arguments @("run", "--rm", "--no-deps", "distributed-audit")

    # A race failure exits with code 66; restart=no keeps the failure visible.
    foreach ($process in $processes) {
        $containerId = & docker @composeArgs ps -a -q $process
        if ($LASTEXITCODE -ne 0 -or -not $containerId) { throw "Missing container: $process" }
        $containerJson = & docker inspect $containerId
        if ($LASTEXITCODE -ne 0) { throw "Cannot inspect: $process" }
        $container = ($containerJson | ConvertFrom-Json)[0]
        if (-not $container.State.Running -or $container.RestartCount -ne 0) {
            throw "Process exited or restarted: $process"
        }
    }
    "PASS: distributed scenarios, SQL audit and seven running race-instrumented processes." |
        Tee-Object -FilePath (Join-Path $evidencePath "result.txt")
} finally {
    # Capture evidence before stopping. No volumes or financial records are removed.
    & docker @composeArgs ps -a | Out-File (Join-Path $evidencePath "containers.txt") -Encoding utf8
    & docker @composeArgs logs --no-color | Out-File (Join-Path $evidencePath "processes.log") -Encoding utf8
    & docker @composeArgs run --rm --no-deps --entrypoint python distributed-test /suite/export_report.py |
        Out-File (Join-Path $evidencePath "report.json") -Encoding utf8
    & docker @composeArgs stop
    Write-Output "Evidence directory: $evidencePath"
}
