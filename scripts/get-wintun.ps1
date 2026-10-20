# Downloads wintun.dll (the TUN driver used by WireGuard) next to mi-client.exe
# and checks that it is signed by WireGuard LLC.
$ErrorActionPreference = "Stop"
$ver  = "0.14.1"
$bin  = Join-Path $PSScriptRoot "..\bin"
$zip  = Join-Path $env:TEMP "wintun-$ver.zip"
$dir  = Join-Path $env:TEMP "wintun-$ver"

New-Item -ItemType Directory -Force $bin | Out-Null
Invoke-WebRequest "https://www.wintun.net/builds/wintun-$ver.zip" -OutFile $zip -UseBasicParsing
Expand-Archive $zip -DestinationPath $dir -Force

$arch = if ([Environment]::Is64BitOperatingSystem) { "amd64" } else { "x86" }
$dll  = Join-Path $dir "wintun\bin\$arch\wintun.dll"
$sig  = Get-AuthenticodeSignature $dll
if ($sig.Status -ne "Valid" -or $sig.SignerCertificate.Subject -notmatch "WireGuard LLC") {
    throw "wintun.dll signature check failed: $($sig.Status) $($sig.SignerCertificate.Subject)"
}
Copy-Item $dll (Join-Path $bin "wintun.dll") -Force
Write-Host "wintun.dll $ver ($arch) -> $bin (signed by WireGuard LLC)"
