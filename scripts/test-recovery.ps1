# Run at the repository root. Copy this entire script into PowerShell if .ps1 files are blocked.
$ErrorActionPreference = "Stop"
$recoveryCompose = @("compose", "-p", "wagering-recovery", "-f", "compose.recovery.yaml")
$recoveryProcesses = @("recovery-a", "recovery-b", "recovery-consumer")
$faultProcesses = @("fault-consumer", "fault-before", "fault-after", "fault-reference")
$evidencePath = Join-Path (Get-Location).Path ("test-results\recovery-" + (Get-Date -Format "yyyyMMdd-HHmmss"))
New-Item -ItemType Directory -Force -Path $evidencePath | Out-Null

function Invoke-RecoveryCompose {
    param([string[]]$Arguments)
    & docker @recoveryCompose @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Recovery command failed: $($Arguments -join ' ')" }
}
function Invoke-RecoveryObserver {
    param([string[]]$Arguments)
    Invoke-RecoveryCompose -Arguments (@("run", "--rm", "--no-deps", "recovery-observer") + $Arguments)
}
function Get-RecoveryContainer {
    param([string]$Service)
    $containerId = & docker @recoveryCompose ps -a -q $Service
    if ($LASTEXITCODE -ne 0 -or -not $containerId) { throw "Missing container: $Service" }
    $raw = & docker inspect $containerId
    if ($LASTEXITCODE -ne 0) { throw "Cannot inspect: $Service" }
    return ($raw | ConvertFrom-Json)[0]
}
function Assert-RecoveryProcesses {
    foreach ($service in (@("api-1") + $recoveryProcesses)) {
        $container = Get-RecoveryContainer -Service $service
        if (-not $container.State.Running -or $container.State.Restarting -or $container.State.OOMKilled) {
            throw "Recovery process is not running normally: $service"
        }
    }
}

try {
    Invoke-RecoveryCompose -Arguments @("config", "--quiet")
    Invoke-RecoveryCompose -Arguments @("build", "api-1", "fault-before", "migrate")
    Invoke-RecoveryCompose -Arguments (@("stop") + $faultProcesses + $recoveryProcesses)
    Invoke-RecoveryCompose -Arguments @("up", "-d", "api-1")
    Invoke-RecoveryCompose -Arguments @("run", "--rm", "publisher-init")
    Invoke-RecoveryCompose -Arguments @("run", "--rm", "ingress-init")

    $scenarios = @(
        @{ Name = "consumer"; Fault = "fault-consumer" },
        @{ Name = "before-send"; Fault = "fault-before" },
        @{ Name = "after-send"; Fault = "fault-after" },
        @{ Name = "reference"; Fault = "fault-reference" }
    )
    foreach ($scenario in $scenarios) {
        Invoke-RecoveryCompose -Arguments (@("stop") + $faultProcesses + $recoveryProcesses)
        Invoke-RecoveryObserver -Arguments @("setup", $scenario.Name)
        Invoke-RecoveryCompose -Arguments @("up", "-d", "--no-deps", $scenario.Fault)
        if ($scenario.Name -eq "consumer") { Invoke-RecoveryObserver -Arguments @("send") }

        # Observe the exact application boundary, then independently verify the SQL commit.
        Invoke-RecoveryObserver -Arguments @("blocked")
        Invoke-RecoveryCompose -Arguments @("run", "--rm", "--no-deps", "recovery-sql", "-f", "/control/current/checkpoint.sql")
        Invoke-RecoveryCompose -Arguments @("kill", "-s", "SIGKILL", $scenario.Fault)
        $killDeadline = [DateTime]::UtcNow.AddSeconds(10)
        do {
            $killed = Get-RecoveryContainer -Service $scenario.Fault
            if (-not $killed.State.Running) { break }
            Start-Sleep -Milliseconds 100
        } while ([DateTime]::UtcNow -lt $killDeadline)
        if ($killed.State.Running -or $killed.State.ExitCode -ne 137 -or $killed.State.OOMKilled) {
            throw "Expected explicit SIGKILL exit 137 without OOM: $($scenario.Fault)"
        }
        $killed.State | ConvertTo-Json -Depth 5 |
            Out-File (Join-Path $evidencePath ($scenario.Name + "-killed.json")) -Encoding utf8

        if ($scenario.Name -eq "after-send") {
            # Observe the already accepted broker message before any recovery publisher starts.
            Invoke-RecoveryObserver -Arguments @("output")
        }
        if ($scenario.Name -eq "reference") { Invoke-RecoveryObserver -Arguments @("reference") }
        Invoke-RecoveryCompose -Arguments (@("up", "-d", "--no-deps") + $recoveryProcesses)
        Invoke-RecoveryObserver -Arguments @("verify")
        if ($scenario.Name -eq "before-send") { Invoke-RecoveryObserver -Arguments @("output") }
        Invoke-RecoveryCompose -Arguments @("run", "--rm", "--no-deps", "recovery-sql", "-f", "/control/current/verify.sql", "-f", "/audit/audit.sql")
        Assert-RecoveryProcesses

        # Preserve PostgreSQL/SQS volumes and restart every active Go process.
        Invoke-RecoveryCompose -Arguments (@("restart", "api-1") + $recoveryProcesses)
        Invoke-RecoveryObserver -Arguments @("restart")
        Invoke-RecoveryCompose -Arguments @("run", "--rm", "--no-deps", "recovery-sql", "-f", "/control/current/verify.sql", "-f", "/audit/audit.sql")
        Assert-RecoveryProcesses
        Invoke-RecoveryObserver -Arguments @("archive")
        & docker @recoveryCompose logs --no-color |
            Out-File (Join-Path $evidencePath ($scenario.Name + "-processes.log")) -Encoding utf8
    }
    "PASS: four controlled SIGKILL scenarios, durable recovery, SQL audits and process restarts." |
        Tee-Object -FilePath (Join-Path $evidencePath "result.txt")
} finally {
    & docker @recoveryCompose ps -a | Out-File (Join-Path $evidencePath "containers.txt") -Encoding utf8
    & docker @recoveryCompose logs --no-color | Out-File (Join-Path $evidencePath "processes.log") -Encoding utf8
    & docker @recoveryCompose run --rm --no-deps recovery-observer export |
        Out-File (Join-Path $evidencePath "report.json") -Encoding utf8
    & docker @recoveryCompose stop
    Write-Output "Evidence directory: $evidencePath"
}
