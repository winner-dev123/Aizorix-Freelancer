# Tear down the Aizorix live demo started by scripts/demo.ps1.
$ROOT = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $ROOT '.demo-pids'

# 1. Kill recorded processes (and their go-run children, which hold the ports).
if(Test-Path $pidFile){
  Get-Content $pidFile | ForEach-Object {
    $procId = $_.Trim()
    if($procId){
      try { Get-CimInstance Win32_Process -Filter "ParentProcessId=$procId" | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -EA SilentlyContinue } } catch {}
      try { Stop-Process -Id $procId -Force -EA SilentlyContinue } catch {}
    }
  }
  Remove-Item $pidFile -Force
}

# 2. Free any service/frontend ports whose child process lingered.
foreach($port in 8080,8081,8082,8084,8085,8086,3000){
  Get-NetTCPConnection -LocalPort $port -State Listen -EA SilentlyContinue | ForEach-Object { try { Stop-Process -Id $_.OwningProcess -Force -EA SilentlyContinue } catch {} }
}
Write-Host "Stopped service + frontend processes." -ForegroundColor Green

# 3. Stop the isolated infra.
docker compose -f (Join-Path $ROOT 'deploy/docker-compose.devrun.yml') down 2>&1 | Out-Null
Write-Host "Infra stopped." -ForegroundColor Green

# 4. Clean the temp web run dir (the '&'-path workaround copy).
$runDir = Join-Path $env:TEMP 'aizorix-web-run'
if(Test-Path $runDir){ Remove-Item -Recurse -Force $runDir -EA SilentlyContinue; Write-Host "Removed temp web run dir." -ForegroundColor Green }
Write-Host "Demo torn down." -ForegroundColor Green
