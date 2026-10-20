# Builds mi-client.exe (Windows) and mi-server for Linux amd64/arm64 into .\bin
# (the app uploads the matching mi-server when it installs a VPS automatically).
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    # Go installed in this session (e.g. by winget) is not on PATH yet.
    $env:Path = "C:\Program Files\Go\bin;" + $env:Path
}
New-Item -ItemType Directory -Force bin | Out-Null

go test ./...
if ($LASTEXITCODE -ne 0) { throw "tests failed" }

$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w" -o bin/mi-client.exe ./cmd/mi-client
if ($LASTEXITCODE -ne 0) { throw "build mi-client failed" }
foreach ($arch in "amd64", "arm64") {
    $env:GOOS = "linux"; $env:GOARCH = $arch
    go build -trimpath -ldflags "-s -w" -o "bin/mi-server-linux-$arch" ./cmd/mi-server
    if ($LASTEXITCODE -ne 0) { throw "build mi-server ($arch) failed" }
}
Copy-Item bin/mi-server-linux-amd64 bin/mi-server -Force   # for manual installs (README)
Copy-Item deploy/server-setup.sh bin/server-setup.sh -Force
Remove-Item Env:GOOS, Env:GOARCH
Get-ChildItem bin
