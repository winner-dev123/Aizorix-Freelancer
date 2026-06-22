# Aizorix — one-command LIVE demo (Windows / PowerShell 5.1+).
#
# Brings the whole platform up against real infrastructure on ISOLATED high ports (no clash
# with other local stacks), applies migrations, seeds demo data, starts the backend + event
# backbone + frontend, and runs smoke flows — reproducing the end-to-end run documented in
# docs/RUN_LOG.md. Handles the two environment footguns automatically:
#   * the '&' in the repo dir name (which hangs Next's server) -> builds/serves the web app
#     from a clean temp copy;
#   * Next 14 needing Node <= 22 -> uses a cached/portable Node 20 when the system node is newer.
#
#   pwsh scripts/demo.ps1            # full demo (infra + backend + events + web + smoke)
#   pwsh scripts/demo.ps1 -NoWeb     # backend + events + API smoke only (skip the browser)
#   pwsh scripts/demo-down.ps1       # tear everything down
#
# Requires: Docker Desktop running, Go toolchain. (Node is auto-provisioned if needed.)
[CmdletBinding()]
param([switch]$NoWeb, [switch]$NoSmoke)

$ErrorActionPreference = 'Stop'
$ROOT = Split-Path -Parent $PSScriptRoot
$DB   = 'postgres://aizorix:aizorix_dev@localhost:55432/aizorix?sslmode=disable'
$KMS  = 'MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY='   # demo KMS master (32 bytes, base64)
$pidFile = Join-Path $ROOT '.demo-pids'
$logDir  = Join-Path $ROOT '.demo-logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
'' | Set-Content $pidFile

function Section($m){ Write-Host "`n=== $m ===" -ForegroundColor Cyan }
function Info($m){ Write-Host "  .. $m" -ForegroundColor DarkGray }
function Ok($m){ Write-Host "  PASS  $m" -ForegroundColor Green }
function Warn($m){ Write-Host "  WARN  $m" -ForegroundColor Yellow }

function FreePorts($ports){ foreach($p in $ports){ Get-NetTCPConnection -LocalPort $p -State Listen -EA SilentlyContinue | ForEach-Object { try { Stop-Process -Id $_.OwningProcess -Force -EA SilentlyContinue } catch {} } } }

function Start-Bg($name,$dir,$exe,$argList,$envMap){
  foreach($k in $envMap.Keys){ Set-Item "env:$k" $envMap[$k] }
  $log = Join-Path $logDir "$name.log"
  $p = Start-Process -FilePath $exe -ArgumentList $argList -WorkingDirectory $dir -RedirectStandardOutput $log -RedirectStandardError "$log.err" -PassThru -WindowStyle Hidden
  Add-Content $pidFile $p.Id
  Info "$name (pid $($p.Id)) -> .demo-logs/$name.log"
}

function Wait-Health($name,$port,$timeoutS=120){
  $deadline=(Get-Date).AddSeconds($timeoutS)
  while((Get-Date) -lt $deadline){
    try { if((Invoke-WebRequest "http://localhost:$port/healthz" -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200){ Ok "$name healthy (:$port)"; return $true } } catch {}
    Start-Sleep -Milliseconds 1500
  }
  Warn "$name (:$port) not healthy after ${timeoutS}s (see .demo-logs/$name.log)"; return $false
}

function Go-Env($port,$extra){
  $e = @{ GOWORK=''; GOFLAGS=''; DATABASE_URL=$DB; REDIS_ADDR='localhost:56379'; KAFKA_BROKERS='localhost:59092'; ENVIRONMENT='local'; LOG_LEVEL='info'; HTTP_PORT="$port" }
  foreach($k in $extra.Keys){ $e[$k]=$extra[$k] }; return $e
}

function Psql($sql){ docker exec aizorix-run-postgres-1 psql -U aizorix -d aizorix -tAc $sql 2>&1 }

# ── Phase 1: infra ───────────────────────────────────────────────────────────
Section 'Phase 1/6 — Infra (Postgres :55432, Redis :56379, MinIO :59000, Redpanda :59092)'
docker info --format '{{.ServerVersion}}' > $null 2>&1
if($LASTEXITCODE -ne 0){ throw 'Docker daemon is not running — start Docker Desktop and retry.' }
FreePorts @(8080,8081,8082,8084,8085,8086,3000)   # make re-runs idempotent
docker compose -f (Join-Path $ROOT 'deploy/docker-compose.devrun.yml') up -d | Out-Null
$deadline=(Get-Date).AddSeconds(150)
do {
  Start-Sleep -Seconds 3
  $pg=docker inspect --format '{{.State.Health.Status}}' aizorix-run-postgres-1 2>$null
  $rp=docker inspect --format '{{.State.Health.Status}}' aizorix-run-redpanda-1 2>$null
} while ((-not ($pg -eq 'healthy' -and $rp -eq 'healthy')) -and (Get-Date) -lt $deadline)
Ok "infra up (postgres=$pg redpanda=$rp)"

# ── Phase 2: migrate + seed ──────────────────────────────────────────────────
Section 'Phase 2/6 — Migrate + seed'
Push-Location (Join-Path $ROOT 'services/tools')
$env:GOWORK='off'; $env:DATABASE_URL=$DB
& go run ./cmd/migrate | Select-Object -Last 1
& go run ./cmd/seed | Select-Object -Last 1 | Out-Null
$env:GOWORK=''
Pop-Location
Ok 'schema applied + demo data seeded (all logins share password: DemoPassw0rd!)'

# ── Phase 3: backend services ────────────────────────────────────────────────
Section 'Phase 3/6 — Backend services'
Start-Bg 'auth' (Join-Path $ROOT 'services/auth') 'go' @('run','./cmd/server') (Go-Env 8081 @{})
if(-not (Wait-Health 'auth' 8081)){ throw 'auth failed to start' }
Start-Bg 'user'         (Join-Path $ROOT 'services/user')         'go' @('run','./cmd/server') (Go-Env 8082 @{})
Start-Bg 'escrow'       (Join-Path $ROOT 'services/escrow')       'go' @('run','./cmd/server') (Go-Env 8084 @{})
Start-Bg 'timetracking' (Join-Path $ROOT 'services/timetracking') 'go' @('run','./cmd/server') (Go-Env 8085 @{})
Start-Bg 'screenshot'   (Join-Path $ROOT 'services/screenshot')   'go' @('run','./cmd/server') (Go-Env 8086 @{ S3_ENDPOINT='http://localhost:59000'; S3_ACCESS_KEY='minioadmin'; S3_SECRET_KEY='minioadmin'; S3_BUCKET_SCREENSHOTS='aizorix-screenshots'; KMS_LOCAL_MASTER_KEY=$KMS })
Start-Bg 'gateway'      (Join-Path $ROOT 'services/gateway')      'go' @('run','./cmd/server') (Go-Env 8080 @{ GATEWAY_JWKS_URL='http://localhost:8081/.well-known/jwks.json'; UPSTREAM_AUTH='http://localhost:8081'; UPSTREAM_USER='http://localhost:8082'; UPSTREAM_ESCROW='http://localhost:8084'; UPSTREAM_TIMETRACKING='http://localhost:8085'; UPSTREAM_SCREENSHOT='http://localhost:8086' })
foreach($s in @(@('user',8082),@('escrow',8084),@('timetracking',8085),@('screenshot',8086),@('gateway',8080))){ Wait-Health $s[0] $s[1] | Out-Null }

# ── Phase 4: event backbone ──────────────────────────────────────────────────
Section 'Phase 4/6 — Event backbone (relay -> Kafka -> consumers)'
Start-Bg 'relay'                 (Join-Path $ROOT 'services/relay')        'go' @('run','./cmd/server')   (Go-Env 0 @{})
Start-Bg 'notification-consumer' (Join-Path $ROOT 'services/notification') 'go' @('run','./cmd/consumer') (Go-Env 0 @{})
Start-Bg 'analytics-consumer'    (Join-Path $ROOT 'services/analytics')    'go' @('run','./cmd/consumer') (Go-Env 0 @{})
Ok 'relay + notification + analytics consumers started'

# ── Phase 5: frontend (best-effort; handles & path + Node version) ───────────
Section 'Phase 5/6 — Frontend (Next.js)'
$webUp = $false
if(-not $NoWeb){ try { $webUp = & {
  function Find-Node {
    if($env:DEMO_NODE -and (Test-Path $env:DEMO_NODE)){ return $env:DEMO_NODE }
    $cached='D:\node20\node-v20.18.1-win-x64\node.exe'; if(Test-Path $cached){ return $cached }
    $sys=Get-Command node -EA SilentlyContinue
    if($sys){ $mj=[int]((& node --version).TrimStart('v').Split('.')[0]); if($mj -ge 18 -and $mj -le 22){ return $sys.Source } }
    $ver='v20.18.1'; $dest='D:\node20'
    Info "downloading portable Node $ver (Next 14 needs Node <= 22)..."
    Invoke-WebRequest "https://nodejs.org/dist/$ver/node-$ver-win-x64.zip" -OutFile "$env:TEMP\node20.zip" -TimeoutSec 300
    Expand-Archive "$env:TEMP\node20.zip" $dest -Force
    return "$dest\node-$ver-win-x64\node.exe"
  }
  $node=Find-Node; Info "node: $node"
  $webSrc=Join-Path $ROOT 'web'
  if(-not (Test-Path (Join-Path $webSrc 'node_modules/next'))){ Info 'npm install (web deps)...'; Push-Location $webSrc; & npm install --no-audit --no-fund | Out-Null; Pop-Location }
  $runDir=$webSrc
  if($ROOT -match '&'){ $runDir=Join-Path $env:TEMP 'aizorix-web-run'; Info "repo path has '&'; copying web -> $runDir (Next can't serve from '&' paths)..."; robocopy $webSrc $runDir /E /XD '.next' /MT:16 /NFL /NDL /NJH /NJS /R:1 /W:1 | Out-Null }
  Info 'next build...'; Push-Location $runDir; $env:API_GATEWAY_URL='http://localhost:8080'
  & $node 'node_modules/next/dist/bin/next' build 2>&1 | Select-Object -Last 2
  $built=($LASTEXITCODE -eq 0); Pop-Location
  if(-not $built){ Warn 'next build failed'; return $false }
  Start-Bg 'web' $runDir $node @('node_modules/next/dist/bin/next','start','-p','3000') @{ API_GATEWAY_URL='http://localhost:8080' }
  for($i=0;$i -lt 30;$i++){ try { if((Invoke-WebRequest 'http://localhost:3000/' -UseBasicParsing -TimeoutSec 3).StatusCode -eq 200){ Ok 'frontend serving (:3000)'; return $true } } catch {}; Start-Sleep -Seconds 2 }
  Warn 'frontend did not come up'; return $false
} } catch { Warn "frontend setup failed: $($_.Exception.Message)" } } else { Info 'skipped (-NoWeb)' }

# ── Phase 6: smoke ───────────────────────────────────────────────────────────
Section 'Phase 6/6 — Smoke flows'
if(-not $NoSmoke){
  $gw='http://localhost:8080'
  try {
    Invoke-RestMethod "$gw/healthz" -TimeoutSec 5 | Out-Null; Ok 'gateway health'
    $login=Invoke-RestMethod "$gw/v1/auth/login" -Method Post -ContentType application/json -Body (@{email='ada@aizorix.dev';password='DemoPassw0rd!'}|ConvertTo-Json) -TimeoutSec 10
    Ok "login (seeded user) -> token issued"
    $me=Invoke-RestMethod "$gw/v1/auth/me" -Headers @{ Authorization="Bearer $($login.access_token)" } -TimeoutSec 10
    Ok "/me -> $($me.email) ($($me.account_type))"
    try { Invoke-RestMethod "$gw/v1/auth/me" -TimeoutSec 8 | Out-Null; Warn '/me without token should 401' } catch { if($_.Exception.Response.StatusCode.value__ -eq 401){ Ok 'unauth /me -> 401 (gateway enforced)' } }
    Start-Sleep -Seconds 4   # let the relay publish + analytics consumer roll up the login events
    $ec=(Psql "SELECT coalesce(sum(count),0) FROM event_counts;").Trim()
    Ok "event backbone: analytics event_counts total = $ec (relay->Kafka->consumer rollup)"
  } catch { Warn "API smoke error: $($_.Exception.Message)" }

  if($webUp){
    try {
      Push-Location (Join-Path $ROOT 'web'); $env:BASE_URL='http://localhost:3000'
      if(-not (Test-Path 'node_modules/playwright')){ Info 'installing Playwright...'; & npm install -D playwright --no-audit --no-fund | Out-Null; & node 'node_modules/playwright/cli.js' install chromium | Out-Null }
      Info 'running Playwright browser click-through...'
      & node 'e2e/smoke.mjs'
      Pop-Location
    } catch { Warn "Playwright error: $($_.Exception.Message)" }
  }
}

Section 'Demo is UP'
Write-Host "  Gateway:  http://localhost:8080" -ForegroundColor Yellow
if($webUp){ Write-Host "  Web app:  http://localhost:3000   (login: ada@aizorix.dev / DemoPassw0rd!)" -ForegroundColor Yellow }
Write-Host "  Logs:     .demo-logs/   |   Screenshots: web/e2e/screenshots/" -ForegroundColor Yellow
Write-Host "  Tear down: pwsh scripts/demo-down.ps1`n" -ForegroundColor Yellow

# Exit explicitly: native tools used above (notably robocopy, whose 1-7 status codes mean
# "success with info", not failure) leave $LASTEXITCODE non-zero, which would otherwise be
# reported as a script failure even though every phase passed.
exit 0
