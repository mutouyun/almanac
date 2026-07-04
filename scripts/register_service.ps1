$nssm = 'C:\services\nssm\nssm.exe'
$exe  = 'C:\services\almanac\almanac.exe'
$name = 'almanac'
$logs = 'C:\services\almanac\logs'

# Remove existing service if present (idempotent). Suppress stderr so a
# "Can't open service!" message (when it does not exist) is not fatal.
& $nssm status $name 2>$null | Out-Null
if ($LASTEXITCODE -eq 0) {
    Write-Output "Service exists, removing first..."
    & $nssm stop $name 2>$null | Out-Null
    & $nssm remove $name confirm 2>$null | Out-Null
    Start-Sleep -Seconds 1
}

# Install service.
& $nssm install $name $exe 2>&1 | Out-Null
& $nssm set $name AppParameters '-addr :8080' 2>&1 | Out-Null
& $nssm set $name AppDirectory 'C:\services\almanac' 2>&1 | Out-Null
& $nssm set $name Start SERVICE_AUTO_START 2>&1 | Out-Null
& $nssm set $name AppStdout "$logs\almanac.out.log" 2>&1 | Out-Null
& $nssm set $name AppStderr "$logs\almanac.err.log" 2>&1 | Out-Null
& $nssm set $name AppRotateFiles 1 2>&1 | Out-Null

# Start it.
& $nssm start $name 2>&1 | Out-Null
Start-Sleep -Seconds 2

$status = & $nssm status $name 2>&1
Write-Output ("SERVICE_STATUS: " + $status)
