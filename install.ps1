# Tor6IA installer for Windows. No admin rights needed.
#   install:    irm https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.ps1 | iex
#   uninstall:  & ([scriptblock]::Create((irm https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.ps1))) -Uninstall
param(
    [string]$Coordinator = "",   # network to join; empty keeps the one built into the exe
    [switch]$NoAutostart,        # do not start Tor6IA when Windows starts
    [switch]$Uninstall
)
$ErrorActionPreference = "Stop"

# Set this to the GitHub repository that publishes the releases.
$Repo = "tomDeprez/Tor6IA"

$dir = Join-Path $env:LOCALAPPDATA "Tor6IA"
$exe = Join-Path $dir "tor6ia.exe"
$menu = Join-Path ([Environment]::GetFolderPath("Programs")) "Tor6IA.lnk"
$startup = Join-Path ([Environment]::GetFolderPath("Startup")) "Tor6IA.lnk"

Get-Process tor6ia -ErrorAction SilentlyContinue | Stop-Process -Force

if ($Uninstall) {
    Remove-Item $menu, $startup -ErrorAction SilentlyContinue
    if (Test-Path $dir) { Remove-Item $dir -Recurse -Force }
    Write-Host "Tor6IA removed. Your account and credits are kept in $env:APPDATA\Tor6IA (delete it to forget them)."
    Write-Host "Ollama and its models are not removed: uninstall Ollama from Windows settings if you do not use it."
    return
}

New-Item -ItemType Directory -Force $dir | Out-Null
$local = if ($PSScriptRoot) { Join-Path $PSScriptRoot "dist\tor6ia.exe" } else { "" }
if ($local -and (Test-Path $local)) {
    Copy-Item $local $exe -Force   # running from a checkout: use the exe just built
} else {
    Write-Host "Downloading Tor6IA..."
    $ProgressPreference = "SilentlyContinue"   # the progress bar makes Invoke-WebRequest very slow
    Invoke-WebRequest "https://github.com/$Repo/releases/latest/download/tor6ia-windows-amd64.exe" -OutFile $exe -UseBasicParsing
}

$runArgs = "start"
if ($Coordinator) { $runArgs += " --coordinator `"$Coordinator`"" }

$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut($menu)
$link.TargetPath = $exe
$link.Arguments = $runArgs
$link.WorkingDirectory = $dir
$link.Save()

if (-not $NoAutostart) {
    $link = $shell.CreateShortcut($startup)
    $link.TargetPath = $exe
    $link.Arguments = "$runArgs --no-browser"
    $link.WorkingDirectory = $dir
    $link.WindowStyle = 7   # minimized
    $link.Save()
}

Write-Host "Tor6IA is installed. Starting it: your browser will open."
Start-Process $exe -ArgumentList $runArgs -WorkingDirectory $dir
