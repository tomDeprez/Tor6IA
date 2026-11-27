# Deploys or updates the Tor6IA coordinator on a Linux server (Debian/Ubuntu with certbot and Apache).
#   ./deploy.ps1 -Server root@203.0.113.10 -Domain tor6ia.example.org
# Login is by SSH key only: no password is read, stored or sent by this script.
# The server side is deploy/remote.sh: it adds its own service and touches nothing else.
param(
    [Parameter(Mandatory)][string]$Server,   # user@host, the user must be root
    [Parameter(Mandatory)][string]$Domain,   # name that points to the server
    [int]$Port = 8443,
    [int]$RelayPort = 8444,                  # joins machines that cannot reach each other; 0 for no relay
    [string]$Key = "$HOME/.ssh/id_ed25519",
    [string]$Version = ""                    # for example v0.2.0; empty keeps the version written in main.go
)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

$ssh = @("-i", $Key, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ConnectTimeout=15")
function Assert-Ok($what) { if ($LASTEXITCODE) { throw "$what failed" } }

go test ./...
Assert-Ok "tests"

$machine = (ssh @ssh $Server "uname -m" | Out-String).Trim()
Assert-Ok "SSH login (is your public key in ~/.ssh/authorized_keys on the server?)"
$arch = @{ "x86_64" = "amd64"; "aarch64" = "arm64" }[$machine]
if (-not $arch) { throw "unsupported server processor: $machine" }

$flags = "-s -w"
if ($Version) { $flags += " -X main.version=$($Version.TrimStart('v'))" }
$env:GOOS, $env:GOARCH = "linux", $arch
try { go build -trimpath -ldflags $flags -o dist/tor6ia-coordinator . } finally { Remove-Item Env:GOOS, Env:GOARCH }
Assert-Ok "build"

$tmp = (ssh @ssh $Server "mktemp -d" | Out-String).Trim()
Assert-Ok "remote temp dir"
scp @ssh -q dist/tor6ia-coordinator deploy/remote.sh "${Server}:$tmp/"
Assert-Ok "upload"
# The relay is the engine release this version of Tor6IA pins, with the checksum written in split.go.
$pins = Get-Content split.go -Raw
$engine = [regex]::Match($pins, 'const engineRelease = "([^"]+)"').Groups[1].Value
$sum = [regex]::Match($pins, '"linux/' + $arch + '":\s+"([0-9a-f]{64})"').Groups[1].Value
if (-not $engine -or -not $sum) { throw "split.go does not pin an engine build for linux/$arch" }
$engineUrl = "https://github.com/tomDeprez/Tor6IA-engine/releases/download/$engine/tor6ia-engine-linux-$arch"
ssh @ssh $Server "bash $tmp/remote.sh '$Domain' $Port $tmp/tor6ia-coordinator $RelayPort '$engineUrl' $sum; code=`$?; rm -rf $tmp; exit `$code"
Assert-Ok "remote install"

$url = "https://${Domain}:$Port"
$reply = (Invoke-WebRequest "$url/" -UseBasicParsing -TimeoutSec 15).Content.Trim()
Write-Host "Public check: $reply"
Write-Host "Coordinator is live: $url"
if ($RelayPort) { Write-Host "Relay: ${Domain}:$RelayPort" }
Write-Host "Build the clients for it:  ./build.ps1 -Coordinator $url"
