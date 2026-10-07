& {
    $ErrorActionPreference = "Stop"
    $perfCompose = @("compose", "-p", "wagering-sqs-performance", "-f", "compose.sqs-performance.yaml")
    $perfProcesses = @("api-1", "api-2", "api-3", "publisher-1", "publisher-2", "ingress-1", "ingress-2")
    $perfStamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $perfDirectory = Join-Path (Get-Location).Path "test-results\sqs-performance-$perfStamp"
    New-Item -ItemType Directory -Path $perfDirectory | Out-Null
    $previousResults = $env:SQS_PERF_RESULTS_PATH
    $previousCase = $env:SQS_LOAD_CASE
    $previousPhase = $env:SQS_LOAD_PHASE
    $previousDuration = $env:SQS_LOAD_SECONDS
    $env:SQS_PERF_RESULTS_PATH = $perfDirectory.Replace('\', '/')
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
            rate = $(if ($env:SQS_LOAD_RATE) { $env:SQS_LOAD_RATE } else { "20" });
            warmupSeconds = 30; measurementSeconds = 120; repetitions = 1;
            topology = "3 APIs, 2 publishers, 2 consumers, PostgreSQL, Keycloak, MiniStack, Go producer, HTTP observer, sampler";
            goImage = "golang:1.27.1-bookworm"; generator = "cmd/load-sqs with AWS SDK from go.mod"; outageSeconds = 20; observerConcurrency = 16; observerRoundDelayMs = 250;
            apiPoolMaxConnections = 10; tracingEnabled = $false; raceEnabled = $false }
        [System.IO.File]::WriteAllText((Join-Path $perfDirectory "environment.json"), ($metadata | ConvertTo-Json -Depth 8), $utf8)
        Invoke-PerformanceCompose -Arguments @("config", "--quiet")
        Invoke-PerformanceCompose -Arguments @("build", "api-1", "migrate")
        Invoke-PerformanceCompose -Arguments (@("up", "-d", "--force-recreate") + $perfProcesses)
        Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "ready")
        Set-PerformanceStage "startup"
        Invoke-PerformanceCompose -Arguments @("up", "-d", "--force-recreate", "sampler")
        foreach ($case in @("many", "duplicates")) {
            $env:SQS_LOAD_CASE = $case
            foreach ($phase in @("warmup", "measure")) {
                $env:SQS_LOAD_PHASE = $phase
                $env:SQS_LOAD_SECONDS = if ($phase -eq "warmup") { "30" } else { "120" }
                Set-PerformanceStage "$case-$phase"
                Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "load")
                Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "audit", "-f", "/results/$case-$phase-inbox.sql")
                Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "queues", "$case-$phase")
            }
            Set-PerformanceStage "$case-drain"
            Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "drain", $case)
            Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "audit")
        }
        $env:SQS_LOAD_CASE = "outage"
        $env:SQS_LOAD_PHASE = "measure"
        $env:SQS_LOAD_SECONDS = "20"
        Set-PerformanceStage "outage-measure"
        try {
            Invoke-PerformanceCompose -Arguments @("stop", "publisher-1", "publisher-2")
            Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "load")
            Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "paused")
        } finally {
            Invoke-PerformanceCompose -Arguments @("up", "-d", "publisher-1", "publisher-2")
        }
        Set-PerformanceStage "outage-drain"
        Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "drain", "outage")
        Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "audit", "-f", "/results/outage-measure-inbox.sql")
        Invoke-PerformanceCompose -Arguments @("run", "--rm", "--no-deps", "tools", "queues", "outage")
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
        "PASS: SQS load, observed terminal latency, inbox audits and publication recovery." |
            Tee-Object -FilePath (Join-Path $perfDirectory "result.txt")
    } finally {
        & docker @perfCompose ps -a | Out-File (Join-Path $perfDirectory "containers.txt") -Encoding utf8
        & docker @perfCompose logs --no-color | Out-File (Join-Path $perfDirectory "processes.log") -Encoding utf8
        & docker @perfCompose images | Out-File (Join-Path $perfDirectory "images.txt") -Encoding utf8
        & docker @perfCompose stop
        $env:SQS_PERF_RESULTS_PATH = $previousResults
        $env:SQS_LOAD_CASE = $previousCase
        $env:SQS_LOAD_PHASE = $previousPhase
        $env:SQS_LOAD_SECONDS = $previousDuration
        Write-Output "Evidence directory: $perfDirectory"
    }
}
