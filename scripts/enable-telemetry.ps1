$ErrorActionPreference = "Stop"
$module = "github.com/Gregory-Pattrick/go-wagering-service/internal/bootstrap"
$utf8WithoutBom = [System.Text.UTF8Encoding]::new($false)
$entrypoints = @(
    @{ Path = "cmd/service/main.go"; Constructor = "New"; Option = "ObserveAPI" },
    @{ Path = "cmd/workers/main.go"; Constructor = "NewWorkers"; Option = "ObserveWorkers" },
    @{ Path = "cmd/consumer/main.go"; Constructor = "NewConsumer"; Option = "ObserveConsumer" }
)
$updates = @()
foreach ($entry in $entrypoints) {
    $path = Join-Path (Get-Location).Path $entry.Path
    $previous = "package main`n`nimport `"$module`"`n`nfunc main() {`n    bootstrap.$($entry.Constructor)().Run()`n}`n"
    $next = "package main`n`nimport `"$module`"`n`nfunc main() {`n    bootstrap.$($entry.Constructor)(bootstrap.$($entry.Option)()).Run()`n}`n"
    $current = [System.IO.File]::ReadAllText($path)
    $normalized = $current -replace '\s', ''
    if ($normalized -ne ($previous -replace '\s', '') -and $normalized -ne ($next -replace '\s', '')) {
        throw "Unexpected custom entrypoint content: $($entry.Path). Review before changing it."
    }
    $updates += @{ Path = $path; Content = $next }
}
# Validate all entrypoints before writing any. Re-running is safe.
foreach ($update in $updates) {
    [System.IO.File]::WriteAllText($update.Path, $update.Content, $utf8WithoutBom)
}
Write-Output "Telemetry enabled for API, workers and consumer entrypoints."
