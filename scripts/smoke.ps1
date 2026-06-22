#requires -version 5.1
<#
.SYNOPSIS
  Smoke test for the Aizorix gateway.

.DESCRIPTION
  Runs four steps and prints PASS/FAIL for each:
    1. GET  /healthz            (retried; the stack may still be warming up)
    2. POST /v1/auth/register   (creates a throwaway freelancer)
    3. POST /v1/auth/login      (obtains an access token)
    4. GET  /v1/auth/me         (authenticated; gateway injects identity)

.EXAMPLE
  ./scripts/smoke.ps1
  ./scripts/smoke.ps1 -GatewayUrl http://localhost:8080
#>
param(
    [string]$GatewayUrl = $(if ($env:GATEWAY_URL) { $env:GATEWAY_URL } else { "http://localhost:8080" })
)

$ErrorActionPreference = "Stop"
$GatewayUrl = $GatewayUrl.TrimEnd("/")
$script:Pass = 0
$script:Fail = 0

function Write-Pass($msg) { Write-Host "PASS " -ForegroundColor Green -NoNewline; Write-Host $msg; $script:Pass++ }
function Write-Fail($msg) { Write-Host "FAIL " -ForegroundColor Red   -NoNewline; Write-Host $msg; $script:Fail++ }

Write-Host "Gateway: $GatewayUrl" -ForegroundColor DarkGray
Write-Host ""

# ── Step 1: health (with retries) ─────────────────────────────────────────────
$healthOk = $false
for ($i = 1; $i -le 10; $i++) {
    try {
        $r = Invoke-WebRequest -Uri "$GatewayUrl/healthz" -Method GET -TimeoutSec 5 -UseBasicParsing
        if ($r.StatusCode -eq 200) { $healthOk = $true; break }
    } catch {
        Write-Host "  health attempt $i/10 failed; retrying..." -ForegroundColor DarkGray
    }
    Start-Sleep -Seconds 2
}
if ($healthOk) { Write-Pass "GET /healthz -> 200" } else { Write-Fail "GET /healthz never returned 200" }

# Unique email so re-runs don't hit EMAIL_TAKEN.
$email    = "smoke+$([DateTimeOffset]::UtcNow.ToUnixTimeSeconds())@aizorix.dev"
$password = "SmokeTest123!"   # >= 12 chars to satisfy the auth service

# Helper: POST/GET JSON, returning @{ code = <int>; body = <psobject|string> }.
function Invoke-Json {
    param([string]$Method, [string]$Url, $Body, [hashtable]$Headers = @{})
    $params = @{ Uri = $Url; Method = $Method; TimeoutSec = 10; UseBasicParsing = $true; Headers = $Headers }
    if ($null -ne $Body) {
        $params.ContentType = "application/json"
        $params.Body = ($Body | ConvertTo-Json -Compress -Depth 6)
    }
    try {
        $resp = Invoke-WebRequest @params
        $parsed = $null
        try { $parsed = $resp.Content | ConvertFrom-Json } catch { $parsed = $resp.Content }
        return @{ code = [int]$resp.StatusCode; body = $parsed }
    } catch {
        $code = -1
        $raw  = $_.Exception.Message
        if ($_.Exception.Response) {
            $code = [int]$_.Exception.Response.StatusCode
            try {
                $sr  = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
                $raw = $sr.ReadToEnd()
            } catch {}
        }
        return @{ code = $code; body = $raw }
    }
}

# ── Step 2: register ──────────────────────────────────────────────────────────
$regBody = @{
    email                          = $email
    password                       = $password
    account_type                   = "freelancer"
    residency_country              = "US"
    locale                         = "en-US"
    accepted_terms                 = $true
    accepted_monitoring_disclosure = $true
}
$reg = Invoke-Json -Method POST -Url "$GatewayUrl/v1/auth/register" -Body $regBody
if ($reg.code -eq 201 -or $reg.code -eq 200) {
    Write-Pass "POST /v1/auth/register -> $($reg.code) ($email)"
} else {
    Write-Fail "POST /v1/auth/register -> $($reg.code): $($reg.body)"
}

# ── Step 3: login ─────────────────────────────────────────────────────────────
$login = Invoke-Json -Method POST -Url "$GatewayUrl/v1/auth/login" -Body @{ email = $email; password = $password }
$accessToken = $null
if ($login.code -eq 200 -and $login.body.access_token) { $accessToken = $login.body.access_token }
if ($accessToken) {
    Write-Pass "POST /v1/auth/login -> 200 (got access token)"
} else {
    Write-Fail "POST /v1/auth/login -> $($login.code): $($login.body)"
}

# ── Step 4: authenticated /me ─────────────────────────────────────────────────
if ($accessToken) {
    $me = Invoke-Json -Method GET -Url "$GatewayUrl/v1/auth/me" -Headers @{ Authorization = "Bearer $accessToken" }
    if ($me.code -eq 200) {
        Write-Pass "GET /v1/auth/me -> 200 ($($me.body.email))"
    } else {
        Write-Fail "GET /v1/auth/me -> $($me.code): $($me.body)"
    }
} else {
    Write-Fail "GET /v1/auth/me skipped (no access token)"
}

Write-Host ""
Write-Host "────────────────────────────────────" -ForegroundColor DarkGray
Write-Host "Result: " -NoNewline
Write-Host "$script:Pass passed" -ForegroundColor Green -NoNewline
Write-Host ", " -NoNewline
Write-Host "$script:Fail failed" -ForegroundColor Red
if ($script:Fail -eq 0) { exit 0 } else { exit 1 }
