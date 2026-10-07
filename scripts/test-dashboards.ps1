& {
    # Run from the repository root after applying and validating the tracing block.
    $ErrorActionPreference = "Stop"
    $dashboardStack = @(
        "compose", "-f", "compose.yaml", "-f", "compose.finance.yaml",
        "-f", "compose.workers.yaml", "-f", "compose.consumer.yaml",
        "-f", "compose.telemetry.yaml", "-f", "compose.tracing.yaml",
        "-f", "compose.observability.yaml", "--profile", "workers",
        "--profile", "consumer", "--profile", "tracing", "--profile", "observability"
    )
    function Invoke-DashboardCompose {
        param([string[]]$Arguments)
        & docker @dashboardStack @Arguments
        if ($LASTEXITCODE -ne 0) { throw "Dashboard command failed: $($Arguments -join ' ')" }
    }
    Invoke-DashboardCompose -Arguments @("config", "--quiet")
    Invoke-DashboardCompose -Arguments @("run", "--rm", "--no-deps", "--entrypoint", "python", "dashboard-check", "-m", "unittest", "discover", "-s", "/observability", "-p", "test_check.py", "-v")
    Invoke-DashboardCompose -Arguments @("run", "--rm", "--no-deps", "--entrypoint", "/bin/promtool", "prometheus", "check", "config", "/etc/prometheus/prometheus.yaml")
    Invoke-DashboardCompose -Arguments @("up", "--build", "-d", "app", "workers", "consumer", "otel-collector", "grafana", "prometheus")
    try {
        Invoke-DashboardCompose -Arguments @("stop", "workers")
        Invoke-DashboardCompose -Arguments @("run", "--rm", "--no-deps", "dashboard-check", "traffic")
    } finally {
        Invoke-DashboardCompose -Arguments @("up", "-d", "workers")
    }
    Invoke-DashboardCompose -Arguments @("run", "--rm", "--no-deps", "dashboard-check", "verify")
    Write-Output "PASS: operational dashboard queries, real traffic, backlog and recovery."
    Write-Output "Grafana: http://127.0.0.1:3000/d/wagering-operations"
}
