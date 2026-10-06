$ErrorActionPreference = "Stop"
$apiBase = if ($env:API_BASE_URL) { $env:API_BASE_URL } else { "http://127.0.0.1:8080" }
$tokenUrl = if ($env:KEYCLOAK_TOKEN_URL) { $env:KEYCLOAK_TOKEN_URL } else { "http://localhost:8081/realms/wagering/protocol/openid-connect/token" }

function Get-ServiceToken([string]$clientId, [string]$secret) {
    $response = Invoke-RestMethod -Method Post -Uri $tokenUrl -ContentType "application/x-www-form-urlencoded" -Body @{
        grant_type = "client_credentials"
        client_id = $clientId
        client_secret = $secret
    }
    return $response.access_token
}

$internalSecret = if ($env:WALLET_CLIENT_SECRET) { $env:WALLET_CLIENT_SECRET } else { "wallet-service-local-secret" }
$providerSecret = if ($env:PROVIDER_A_CLIENT_SECRET) { $env:PROVIDER_A_CLIENT_SECRET } else { "provider-a-local-secret" }
$internalHeaders = @{ Authorization = "Bearer $(Get-ServiceToken 'wallet-service' $internalSecret)" }
$providerToken = Get-ServiceToken "provider-a" $providerSecret
$playerId = [guid]::NewGuid().ToString()
$opening = @{ playerId = $playerId; initialBalance = @{ amount = "100.00"; currency = "BRL" } } | ConvertTo-Json -Depth 6
$wallet = Invoke-RestMethod -Method Post -Uri "$apiBase/wallets" -Headers $internalHeaders -ContentType "application/json" -Body $opening
if ($wallet.balance.amount -ne "100.00" -or $wallet.version -ne 1) { throw "Opening assertion failed" }

$externalId = [guid]::NewGuid().ToString()
$betHeaders = @{ Authorization = "Bearer $providerToken"; "Idempotency-Key" = "provider-a:$externalId" }
$bet = @{
    providerId = "provider-a"
    externalTransactionId = $externalId
    playerId = $playerId
    walletId = $wallet.id
    roundId = "smoke-round"
    gameId = "smoke-game"
    kind = "BET"
    money = @{ amount = "80.00"; currency = "BRL" }
} | ConvertTo-Json -Depth 6
$first = Invoke-RestMethod -Method Post -Uri "$apiBase/wagering/transactions" -Headers $betHeaders -ContentType "application/json" -Body $bet
if ($first.status -ne "PROCESSED" -or $first.balance.amount -ne "20.00" -or $first.idempotentReplay) { throw "BET assertion failed" }

$winId = [guid]::NewGuid().ToString()
$winHeaders = @{ Authorization = "Bearer $providerToken"; "Idempotency-Key" = "provider-a:$winId" }
$win = @{
    providerId = "provider-a"
    externalTransactionId = $winId
    playerId = $playerId
    walletId = $wallet.id
    roundId = "smoke-round"
    gameId = "smoke-game"
    kind = "WIN"
    money = @{ amount = "30.00"; currency = "BRL" }
} | ConvertTo-Json -Depth 6
$null = Invoke-RestMethod -Method Post -Uri "$apiBase/wagering/transactions" -Headers $winHeaders -ContentType "application/json" -Body $win
$replay = Invoke-RestMethod -Method Post -Uri "$apiBase/wagering/transactions" -Headers $betHeaders -ContentType "application/json" -Body $bet
if (-not $replay.idempotentReplay -or $replay.transactionId -ne $first.transactionId -or $replay.balance.amount -ne "20.00") { throw "Original replay snapshot assertion failed" }
$latest = Invoke-RestMethod -Uri "$apiBase/wallets/$($wallet.id)" -Headers $internalHeaders
if ($latest.balance.amount -ne "50.00" -or $latest.version -ne 3) { throw "Final wallet assertion failed" }
$check = Invoke-RestMethod -Method Post -Uri "$apiBase/wallets/$($wallet.id)/reconciliation" -Headers $internalHeaders
if (-not $check.consistent -or $check.difference.amount -ne "0.00" -or $check.checkedEntries -ne 3) { throw "Reconciliation assertion failed" }
Write-Output "PASS: opening, BET, WIN, original-result replay and reconciliation."
Write-Output "Wallet ID: $($wallet.id)"
