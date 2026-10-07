& {
    $ErrorActionPreference = "Stop"
    $perfCompose = @("compose", "-p", "wagering-performance", "-f", "compose.performance.yaml")
    $perfProcesses = @("api-1", "api-2", "api-3", "publisher-1", "publisher-2")
    $perfStamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $perfDirectory = Join-Path (Get-Location).Path "test-results\performance-$perfStamp"
    New-Item -ItemType Directory -Path $perfDirectory | Out-Null
    $previousResults = $env:PERF_RESULTS_PATH
    $previousCase = $env:PERF_CASE
    $previousPhase = $env:PERF_PHASE
    $previousDuration = $env:PERF_DURATION
    $env:PERF_RESULTS_PATH = $perfDirectory.Replace('\', '/')
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    function Invoke-PerformanceCompose {
        param([string[]]$Arguments)
        & docker @perfCompose @Arguments
        if ($LASTEXITCODE -ne 0) { throw "Performance command failed: $($Arguments -join ' ')" }
    }
    function Set-PerformanceStage {
        param([string]$Stage)
        [System.IO.File]::WriteAllText((Join-Path $perfDirectory "stage.txt"), $Stage, $utf8)
    }
    try {
        $commit = & git rev-parse HEAD
        if ($LASTEXITCODE -ne 0) { throw "Cannot read Git revision" }
        $dirty = & git status --porcelain
        if ($LASTEXITCODE -ne 0) { throw "Cannot read Git status" }
        $engineRaw = & docker info --format '{{json .}}'
        if ($LASTEXITCODE -ne 0) { throw "Docker is unavailable" }
        $engine = $engineRaw | ConvertFrom-Json
        $cpu = Get-CimInstance Win32_Processor | Select-Object Name, NumberOfCores, NumberOfLogicalProcessors
        $memory = (Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory
        $metadata = @{ commit = "$commit"; workingTreeChanges = @($dirty); hostCPU = @($cpu);
            hostRAMBytes = $memory; dockerCPUs = $engine.NCPU; dockerRAMBytes = $engine.MemTotal;
            dockerVersion = $engine.ServerVersion; composeVersion = (& docker compose version --short);
            rate = $(if ($env:PERF_RATE) { $env:PERF_RATE } else { "20" });
            warmupSeconds = 30; measurementSeconds = 120; repetitions = 1;
            topology = "3 APIs, 2 publishers, PostgreSQL, Keycloak, MiniStack, k6, metrics sampler";
            goImage = "golang:1.27.1-bookworm"; k6Image = "grafana/k6:1.3.0";
            apiPoolMaxConnections = 10; tracingEnabled = $false; raceEnabled = $false }
        [System.IO.File]::WriteAllText((Join-Path $perfDirectory "environment.json"), ($metadata | ConvertTo-Json -Depth 8), $utf8)
        Invoke-PerformanceCompose -Arguments @("config", "--quiet")
        Invoke-PerformanceCompose -Arguments @("build", "api-1", "migrate")
        Invoke-PerformanceCompose -Arguments (@("up", "-d", "--force-recreate") + $perfProcesses)
        Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "ready")
        Set-PerformanceStage "startup"
        Invoke-PerformanceCompose -Arguments @("up", "-d", "--force-recreate", "sampler")
        foreach ($case in @("many", "hot", "replay")) {
            $env:PERF_CASE = $case
            foreach ($phase in @("warmup", "measure")) {
                $env:PERF_PHASE = $phase
                $env:PERF_DURATION = if ($phase -eq "warmup") { "30" } else { "120" }
                Set-PerformanceStage "$case-$phase"
                Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "load", "run", "/suite/http.js")
            }
            Set-PerformanceStage "$case-drain"
            Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "drain", $case)
            Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "audit")
        }
        foreach ($process in $perfProcesses) {
            $id = & docker @perfCompose ps -a -q $process
            if ($LASTEXITCODE -ne 0 -or -not $id) { throw "Missing process: $process" }
            $state = & docker inspect --format '{{json .State}}' $id
            if ($LASTEXITCODE -ne 0) { throw "Cannot inspect process: $process" }
            $state = $state | ConvertFrom-Json
            if (-not $state.Running -or $state.OOMKilled) { throw "Unhealthy process: $process" }
            $restarts = & docker inspect --format '{{.RestartCount}}' $id
            if ($LASTEXITCODE -ne 0 -or [int]$restarts -ne 0) { throw "Process restarted: $process" }
        }
        Invoke-PerformanceCompose -Arguments @("stop", "sampler")
        Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "report")
        "PASS: HTTP load scenarios, post-load reconciliation, SQL audits and measured report." |
            Tee-Object -FilePath (Join-Path $perfDirectory "result.txt")
    } finally {
        & docker @perfCompose ps -a | Out-File (Join-Path $perfDirectory "containers.txt") -Encoding utf8
        & docker @perfCompose logs --no-color | Out-File (Join-Path $perfDirectory "processes.log") -Encoding utf8
        & docker @perfCompose images | Out-File (Join-Path $perfDirectory "images.txt") -Encoding utf8
        & docker @perfCompose stop
        $env:PERF_RESULTS_PATH = $previousResults
        $env:PERF_CASE = $previousCase
        $env:PERF_PHASE = $previousPhase
        $env:PERF_DURATION = $previousDuration
        Write-Output "Evidence directory: $perfDirectory"
    }
}
