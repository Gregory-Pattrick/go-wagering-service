& {
    $ErrorActionPreference = "Stop"
    $brokerArgs = @("compose", "-p", "wagering-broker-security", "-f", "compose.broker-security.yaml")
    function Invoke-BrokerCheck {
        param([string[]]$Arguments)
        & docker @brokerArgs @Arguments
        if ($LASTEXITCODE -ne 0) { throw "Broker verification failed: $($Arguments -join ' ')" }
    }
    try {
        Invoke-BrokerCheck -Arguments @("config", "--quiet")
        Invoke-BrokerCheck -Arguments @("up", "--build", "-d", "--wait", "--wait-timeout", "120", "ministack")
        Invoke-BrokerCheck -Arguments @("run", "--rm", "security-tests", "/scripts/bootstrap.py")
        Invoke-BrokerCheck -Arguments @("run", "--rm", "security-tests", "/scripts/verify.py")
        Invoke-BrokerCheck -Arguments @("run", "--rm", "security-tests", "/scripts/verify_signatures.py")
        Invoke-BrokerCheck -Arguments @("run", "--rm", "security-tests", "/scripts/verify_iam.py")
        Invoke-BrokerCheck -Arguments @("stop", "ministack")
        Invoke-BrokerCheck -Arguments @("up", "-d", "--wait", "--wait-timeout", "120", "ministack")
        Invoke-BrokerCheck -Arguments @("run", "--rm", "security-tests", "/scripts/verify_signatures.py")
        Invoke-BrokerCheck -Arguments @("run", "--rm", "security-tests", "/scripts/verify_iam.py")
        Write-Output "PASS: broker signatures, IAM policies and graceful-restart persistence."
    } finally {
        & docker @brokerArgs stop ministack
        if ($LASTEXITCODE -ne 0) { Write-Warning "Could not stop isolated security broker." }
    }
}
