$path = [Environment]::GetEnvironmentVariable('Path', 'Machine')
$target = 'C:\services\nssm'

if ($path -notlike "*$target*") {
    [Environment]::SetEnvironmentVariable('Path', $path + ";$target", 'Machine')
    Write-Output "PATH_ADDED: $target"
} else {
    Write-Output "PATH_ALREADY_PRESENT: $target"
}

# Verify by spawning a new process (PATH changes need new session to take effect)
$v = & $target\nssm.exe --version 2>&1
if ($LASTEXITCODE -eq 0) {
    Write-Output ("NSSM_VERSION: " + ($v -split "`n")[0])
} else {
    Write-Output "NSSM_NOT_WORKING"
}
