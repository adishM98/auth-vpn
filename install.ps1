#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Installs or upgrades auth-vpn on Windows.

.DESCRIPTION
    Downloads auth-vpn.exe and wintun.dll from the latest GitHub release,
    installs them to %ProgramFiles%\auth-vpn, and adds that directory to the
    system PATH. Requires Administrator privileges (needed for wintun kernel
    driver installation on first connect).

.PARAMETER Version
    Pin to a specific release tag, e.g. "v2.3.0". Defaults to latest.

.EXAMPLE
    # One-liner (PowerShell as Administrator):
    irm https://github.com/adishM98/auth-vpn/releases/latest/download/install.ps1 | iex

    # Pin to a version:
    $env:VERSION = "v2.3.0"
    irm https://github.com/adishM98/auth-vpn/releases/latest/download/install.ps1 | iex
#>

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$Repo      = "adishM98/auth-vpn"
$InstallDir = "$env:ProgramFiles\auth-vpn"
$Version   = if ($env:VERSION) { $env:VERSION } else { "latest" }

function Write-Step($msg) { Write-Host "  -> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)   { Write-Host "  v  $msg" -ForegroundColor Green }
function Write-Warn($msg) { Write-Host "  !  $msg" -ForegroundColor Yellow }

Write-Host ""
Write-Host "  auth-vpn installer for Windows" -ForegroundColor Bold
Write-Host "  ────────────────────────────────────────────"
Write-Host ""

# ── resolve version ──────────────────────────────────────────────────────────

if ($Version -eq "latest") {
    Write-Step "Fetching latest release info..."
    $release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest" `
        -Headers @{ Accept = "application/vnd.github+json" }
    $Version = $release.tag_name
}
Write-Ok "Version: $Version"

$BaseUrl = "https://github.com/$Repo/releases/download/$Version"

# ── create install directory ─────────────────────────────────────────────────

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir | Out-Null
}
Write-Ok "Install directory: $InstallDir"

# ── download files ───────────────────────────────────────────────────────────

$files = @(
    @{ url = "$BaseUrl/auth-vpn-windows-amd64.exe"; dest = "$InstallDir\auth-vpn.exe" },
    @{ url = "$BaseUrl/wintun-amd64.dll";           dest = "$InstallDir\wintun.dll"   }
)

foreach ($f in $files) {
    $name = Split-Path $f.dest -Leaf
    Write-Step "Downloading $name ..."
    $tmp = "$($f.dest).new"
    try {
        Invoke-WebRequest -Uri $f.url -OutFile $tmp -UseBasicParsing
    } catch {
        throw "Download failed for $name : $_"
    }

    # Rename trick: move old file aside (handles locked DLLs / running exe),
    # then place new file. Clean up .old from previous update.
    $old = "$($f.dest).old"
    if (Test-Path $old) { Remove-Item $old -Force -ErrorAction SilentlyContinue }
    if (Test-Path $f.dest) { Move-Item $f.dest $old -Force }
    Move-Item $tmp $f.dest -Force
    Write-Ok "$name installed"
}

# ── add to system PATH ───────────────────────────────────────────────────────

$sysPath = [Environment]::GetEnvironmentVariable("PATH", "Machine")
if ($sysPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("PATH", "$sysPath;$InstallDir", "Machine")
    Write-Ok "Added $InstallDir to system PATH"
} else {
    Write-Ok "PATH already contains $InstallDir"
}

# ── done ─────────────────────────────────────────────────────────────────────

Write-Host ""
Write-Host "  auth-vpn $Version installed!" -ForegroundColor Green
Write-Host "  ────────────────────────────────────────────"
Write-Host ""
Write-Host "  Open a new PowerShell window and connect:"
Write-Host ""
Write-Host "    auth-vpn connect <server-ip>:7777 --token <token>"
Write-Host ""
Write-Host "  Background mode (for CI):"
Write-Host ""
Write-Host "    auth-vpn connect <server-ip>:7777 --token <token> --background --reconnect"
Write-Host ""
Write-Host "  NOTE: The first connection installs the Wintun kernel driver."
Write-Host "        Run PowerShell as Administrator for that first connect."
Write-Host "        Subsequent connects do not require Administrator."
Write-Host ""
