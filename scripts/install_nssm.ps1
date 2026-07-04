$ErrorActionPreference = 'Stop'
$dir = 'C:\services\nssm'
New-Item -ItemType Directory -Path $dir -Force | Out-Null
$zip = Join-Path $dir 'nssm.zip'

$sources = @(
    'https://nssm.cc/release/nssm-2.24.zip',
    'https://github.com/kirillkovalenko/nssm/releases/download/2.24/nssm-2.24.zip'
)

$ok = $false
foreach ($url in $sources) {
    try {
        Write-Output ("Trying: " + $url)
        Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing -TimeoutSec 30
        if ((Get-Item $zip).Length -gt 100000) { $ok = $true; break }
    } catch {
        Write-Output ("  failed: " + $_.Exception.Message)
    }
}

if (-not $ok) { Write-Output 'ALL_SOURCES_FAILED'; exit 1 }

Expand-Archive -Path $zip -DestinationPath (Join-Path $dir 'extract') -Force
$exe = Get-ChildItem -Path (Join-Path $dir 'extract') -Recurse -Filter 'nssm.exe' |
    Where-Object { $_.FullName -match 'win64' } | Select-Object -First 1
Copy-Item $exe.FullName (Join-Path $dir 'nssm.exe') -Force

if (Test-Path (Join-Path $dir 'nssm.exe')) {
    Write-Output ('NSSM_INSTALLED: ' + (Join-Path $dir 'nssm.exe'))
} else {
    Write-Output 'COPY_FAILED'; exit 1
}
