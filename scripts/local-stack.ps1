param(
    [ValidateSet("start", "stop", "status", "smoke")][string]$Action = "start",
    [ValidatePattern("^[a-z0-9][a-z0-9_-]+$")][string]$ProjectName = "go-wagering-service"
)

& {
    $ErrorActionPreference = "Stop"
    if (-not (Test-Path -LiteralPath ".\compose.observability.yaml")) {
        throw "Run this script from the repository root."
    }
    $localArgs = @(
        "compose", "-p", $ProjectName,
        "-f", "compose.yaml", "-f", "compose.finance.yaml",
        "-f", "compose.workers.yaml", "-f", "compose.consumer.yaml",
        "-f", "compose.telemetry.yaml", "-f", "compose.tracing.yaml",
        "-f", "compose.observability.yaml",
        "--profile", "workers", "--profile", "consumer",
        "--profile", "tracing", "--profile", "observability"
    )
    function Invoke-LocalCompose {
        param([string[]]$Arguments)
        & docker @localArgs @Arguments
        if ($LASTEXITCODE -ne 0) { throw "Local stack command failed: $($Arguments -join ' ')" }
    }
    function Wait-LocalReady {
        $deadline = (Get-Date).AddSeconds(120)
        $urls = @("http://127.0.0.1:8080/health/ready", "http://127.0.0.1:9090/health/ready",
                  "http://127.0.0.1:9091/health/ready", "http://127.0.0.1:9092/health/ready")
        foreach ($url in $urls) {
            $ready = $false
            while ((Get-Date) -lt $deadline) {
                try {
                    $response = Invoke-RestMethod -Uri $url -TimeoutSec 5
                    if ($response.status -eq "ok") { $ready = $true; break }
                } catch { }
                Start-Sleep -Seconds 1
            }
            if (-not $ready) { throw "Readiness timeout: $url" }
        }
    }
    Invoke-LocalCompose -Arguments @("config", "--quiet")
    switch ($Action) {
        "start" {
            # Build before interrupting any running development processes.
            Invoke-LocalCompose -Arguments @("build", "ministack", "app", "workers", "consumer", "trace-migrate")
            # The old broker must shut down cleanly to save its in-memory state.
            Invoke-LocalCompose -Arguments @("stop", "app", "workers", "consumer", "ministack")
            Invoke-LocalCompose -Arguments @("up", "-d", "--wait", "--wait-timeout", "300", "app", "workers", "consumer", "otel-collector", "grafana", "prometheus")
            Wait-LocalReady
            Invoke-LocalCompose -Arguments @("ps")
            Write-Output "PASS: migrations, API, workers, consumer and dependency readiness."
            Write-Output "API: http://127.0.0.1:8080"
            Write-Output "Grafana: http://127.0.0.1:3000/d/wagering-operations"
        }
        "smoke" {
            Wait-LocalReady
            # Bind the HTTP smoke to this local stack, restoring caller settings.
            $previousApi = $env:API_BASE_URL
            $previousToken = $env:KEYCLOAK_TOKEN_URL
            try {
                $env:API_BASE_URL = "http://127.0.0.1:8080"
                $env:KEYCLOAK_TOKEN_URL = "http://localhost:8081/realms/wagering/protocol/openid-connect/token"
                & ([scriptblock]::Create((Get-Content -Raw -LiteralPath ".\scripts\smoke-api.ps1")))
                Invoke-LocalCompose -Arguments @("run", "--rm", "consumer-smoke")
            } finally {
                $env:API_BASE_URL = $previousApi
                $env:KEYCLOAK_TOKEN_URL = $previousToken
            }
            Write-Output "PASS: authenticated HTTP and SQS smoke tests."
        }
        "status" { Invoke-LocalCompose -Arguments @("ps") }
        "stop" { Invoke-LocalCompose -Arguments @("stop") }
    }
}
