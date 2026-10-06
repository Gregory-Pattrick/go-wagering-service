# Run after applying the tracing block and resolving its pinned Go modules.
$ErrorActionPreference = "Stop"
$tracingStack = @(
    "compose", "-f", "compose.yaml", "-f", "compose.finance.yaml",
    "-f", "compose.workers.yaml", "-f", "compose.consumer.yaml",
    "-f", "compose.telemetry.yaml", "-f", "compose.tracing.yaml",
    "--profile", "workers", "--profile", "consumer", "--profile", "tracing", "--profile", "financial-testing", "--profile", "tracing-testing"
)
function Invoke-TracingCompose {
    param([string[]]$Arguments)
    & docker @tracingStack @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Tracing command failed: $($Arguments -join ' ')" }
}
Invoke-TracingCompose -Arguments @("config", "--quiet")
Invoke-TracingCompose -Arguments @("up", "--build", "-d", "app", "workers", "consumer", "otel-collector", "grafana")
Invoke-TracingCompose -Arguments @("run", "--rm", "--no-deps", "tracing-smoke", "healthy")
try {
    Invoke-TracingCompose -Arguments @("stop", "otel-collector")
    Invoke-TracingCompose -Arguments @("run", "--rm", "--no-deps", "tracing-smoke", "collector-down")
} finally {
    # Restore the collector even if a financial assertion fails.
    Invoke-TracingCompose -Arguments @("up", "-d", "otel-collector")
}
Invoke-TracingCompose -Arguments @("run", "--rm", "--no-deps", "tracing-smoke", "healthy")
Write-Output "PASS: end-to-end traces, collector outage isolation and resumed export."
