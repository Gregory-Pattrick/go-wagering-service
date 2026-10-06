$ErrorActionPreference = "Stop"
$apiBase = if ($env:API_BASE_URL) { $env:API_BASE_URL } else { "http://127.0.0.1:8080" }
$tokenUrl = if ($env:KEYCLOAK_TOKEN_URL) { $env:KEYCLOAK_TOKEN_URL } else { "http://localhost:8081/realms/wagering/protocol/openid-connect/token" }

function Get-WorkerTestToken([string]$clientId, [string]$secret) {
    $response = Invoke-RestMethod -Method Post -Uri $tokenUrl -ContentType "application/x-www-form-urlencoded" -Body @{
        grant_type = "client_credentials"
        client_id = $clientId
        client_secret = $secret
    }
    return $response.access_token
}
$internalSecret = if ($env:WALLET_CLIENT_SECRET) { $env:WALLET_CLIENT_SECRET } else { "wallet-service-local-secret" }
$providerSecret = if ($env:PROVIDER_A_CLIENT_SECRET) { $env:PROVIDER_A_CLIENT_SECRET } else { "provider-a-local-secret" }
$internalHeaders = @{ Authorization = "Bearer $(Get-WorkerTestToken 'wallet-service' $internalSecret)" }
$providerToken = Get-WorkerTestToken "provider-a" $providerSecret
$providerHeaders = @{ Authorization = "Bearer $providerToken" }
$playerId = [guid]::NewGuid().ToString()
$opening = @{ playerId = $playerId; initialBalance = @{ amount = "100.00"; currency = "BRL" } } | ConvertTo-Json -Depth 6
$wallet = Invoke-RestMethod -Method Post -Uri "$apiBase/wallets" -Headers $internalHeaders -ContentType "application/json" -Body $opening
$walletId = ([guid]$wallet.id).ToString()
$betId = [guid]::NewGuid().ToString()
$refundId = [guid]::NewGuid().ToString()
$refundHeaders = @{ Authorization = "Bearer $providerToken"; "Idempotency-Key" = "provider-a:$refundId" }
$refund = @{
    providerId = "provider-a"
    externalTransactionId = $refundId
    playerId = $playerId
    walletId = $walletId
    roundId = "worker-smoke-round"
    gameId = "worker-smoke-game"
    kind = "REFUND"
    money = @{ amount = "80.00"; currency = "BRL" }
    referenceExternalTransactionId = $betId
} | ConvertTo-Json -Depth 6
$pending = Invoke-RestMethod -Method Post -Uri "$apiBase/wagering/transactions" -Headers $refundHeaders -ContentType "application/json" -Body $refund
if ($pending.status -ne "PENDING_REFERENCE") { throw "Refund must wait for the missing BET" }
$unchanged = Invoke-RestMethod -Uri "$apiBase/wallets/$walletId" -Headers $internalHeaders
if ($unchanged.balance.amount -ne "100.00" -or $unchanged.version -ne 1) { throw "Pending reference moved the balance" }
$betHeaders = @{ Authorization = "Bearer $providerToken"; "Idempotency-Key" = "provider-a:$betId" }
$bet = @{
    providerId = "provider-a"
    externalTransactionId = $betId
    playerId = $playerId
    walletId = $walletId
    roundId = "worker-smoke-round"
    gameId = "worker-smoke-game"
    kind = "BET"
    money = @{ amount = "80.00"; currency = "BRL" }
} | ConvertTo-Json -Depth 6
$processedBet = Invoke-RestMethod -Method Post -Uri "$apiBase/wagering/transactions" -Headers $betHeaders -ContentType "application/json" -Body $bet
if ($processedBet.status -ne "PROCESSED") { throw "BET was not processed" }
$deadline = [DateTime]::UtcNow.AddSeconds(60)
do {
    $result = Invoke-RestMethod -Uri "$apiBase/wagering/transactions/$($pending.transactionId)" -Headers $providerHeaders
    if ($result.status -eq "PROCESSED") { break }
    if ($result.status -ne "PENDING_REFERENCE") { throw "Unexpected reference status: $($result.status)" }
    Start-Sleep -Milliseconds 500
} while ([DateTime]::UtcNow -lt $deadline)
if ($result.status -ne "PROCESSED") { throw "Reference worker did not complete within 60 seconds" }
$replay = Invoke-RestMethod -Method Post -Uri "$apiBase/wagering/transactions" -Headers $refundHeaders -ContentType "application/json" -Body $refund
if (-not $replay.idempotentReplay -or $replay.transactionId -ne $pending.transactionId -or $replay.balance.amount -ne "100.00") { throw "Resumed refund replay failed" }
$latest = Invoke-RestMethod -Uri "$apiBase/wallets/$walletId" -Headers $internalHeaders
if ($latest.balance.amount -ne "100.00" -or $latest.version -ne 3) { throw "Final wallet assertion failed" }
$check = Invoke-RestMethod -Method Post -Uri "$apiBase/wallets/$walletId/reconciliation" -Headers $internalHeaders
if (-not $check.consistent -or $check.difference.amount -ne "0.00" -or $check.checkedEntries -ne 3) { throw "Reconciliation failed" }
$deadline = [DateTime]::UtcNow.AddSeconds(60)
do {
    $remaining = docker compose exec -T postgres psql -U postgres -d wagering -At -c "SELECT count(*) FROM wagering.outbox WHERE aggregate_id='$walletId' AND published_at IS NULL;"
    if ($LASTEXITCODE -ne 0) { throw "Unable to inspect the local outbox" }
    if (([string]$remaining).Trim() -eq "0") { break }
    Start-Sleep -Milliseconds 500
} while ([DateTime]::UtcNow -lt $deadline)
if (([string]$remaining).Trim() -ne "0") { throw "Outbox did not drain within 60 seconds" }
$total = docker compose exec -T postgres psql -U postgres -d wagering -At -c "SELECT count(*) FROM wagering.outbox WHERE aggregate_id='$walletId' AND published_at IS NOT NULL;"
if ($LASTEXITCODE -ne 0 -or ([string]$total).Trim() -ne "7") { throw "Expected seven published events" }
Write-Output "PASS: pending reference, automatic refund, replay, reconciliation and seven published events."
Write-Output "Wallet ID: $walletId"
