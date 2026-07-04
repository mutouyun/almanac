$ErrorActionPreference = 'Stop'
$ver = 'go1.26.4'
$url = "https://go.dev/dl/$ver.windows-amd64.zip"
$zip = "$env:TEMP\$ver.zip"
$dest = 'C:\Go-local'

Write-Output "Downloading $url ..."
Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing -TimeoutSec 300
Write-Output ("Downloaded: " + [math]::Round((Get-Item $zip).Length/1MB,1) + " MB")

if (Test-Path $dest) { Remove-Item $dest -Recurse -Force }
Write-Output "Extracting to $dest ..."
Expand-Archive -Path $zip -DestinationPath $dest -Force
# zip contains a top-level 'go' folder -> C:\Go-local\go\bin\go.exe

$goExe = "$dest\go\bin\go.exe"
if (Test-Path $goExe) {
    Write-Output "GO_INSTALLED"
    & $goExe version
} else {
    Write-Output "GO_MISSING_AFTER_EXTRACT"
}
Remove-Item $zip -ErrorAction SilentlyContinue
