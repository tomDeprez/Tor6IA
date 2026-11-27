# Builds the release binaries into dist/. Runs on Windows and, with pwsh, on Linux and macOS.
#   ./build.ps1                                      solo-mode binaries
#   ./build.ps1 -Coordinator https://your.network    binaries that join your network by default
param(
    [string]$Coordinator = "",
    [string]$Version = ""   # for example v0.2.0; empty keeps the version written in main.go
)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

go test ./...
if ($LASTEXITCODE) { throw "tests failed" }

$flags = "-s -w -X main.defaultCoordinator=$Coordinator"
if ($Version) { $flags += " -X main.version=$($Version.TrimStart('v'))" }

$targets = @("windows/amd64", "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")
try {
    foreach ($t in $targets) {
        $env:GOOS, $env:GOARCH = $t.Split("/")
        $out = "dist/tor6ia-$($env:GOOS)-$($env:GOARCH)"
        if ($env:GOOS -eq "windows") { $out += ".exe" }
        go build -trimpath -ldflags $flags -o $out .
        if ($LASTEXITCODE) { throw "build failed for $t" }
        Write-Host "built $out"
    }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}
Copy-Item dist/tor6ia-windows-amd64.exe dist/tor6ia.exe -Force
