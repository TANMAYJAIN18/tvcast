# TV Cast installer for Windows 10/11.
#   irm https://raw.githubusercontent.com/TANMAYJAIN18/tvcast/main/install.ps1 | iex
# Safe to run again any time: it updates/repairs and keeps your TVs and settings.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Repo    = 'TANMAYJAIN18/tvcast'
$Release = "https://github.com/$Repo/releases/latest/download"
$Raw     = "https://raw.githubusercontent.com/$Repo/main"
$Dir     = Join-Path $env:LOCALAPPDATA 'TVCast'
$Exe     = Join-Path $Dir 'TVCast.exe'
$Icon    = Join-Path $Dir 'TVCast.ico'

function Ok($m)  { Write-Host " ✓ $m" -ForegroundColor Green }
function Say($m) { Write-Host "==> $m" -ForegroundColor Cyan }

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' }
Say "Installing TV Cast for Windows ($arch)"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null

Get-Process -Name 'TVCast' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

$local = if ($PSScriptRoot) { Join-Path $PSScriptRoot "dist\TVCast-windows-$arch.exe" } else { '' }
if ($local -and (Test-Path $local)) { Copy-Item $local $Exe -Force }
else { Invoke-WebRequest -UseBasicParsing "$Release/TVCast-windows-$arch.exe" -OutFile $Exe }
try { Invoke-WebRequest -UseBasicParsing "$Raw/assets/icon.ico" -OutFile $Icon } catch {}
Unblock-File $Exe -ErrorAction SilentlyContinue
Ok "Installed $Exe"

$shell = New-Object -ComObject WScript.Shell
function Make-Shortcut($path, $arguments) {
  $s = $shell.CreateShortcut($path)
  $s.TargetPath = $Exe
  $s.Arguments = $arguments
  $s.WorkingDirectory = $Dir
  if (Test-Path $Icon) { $s.IconLocation = $Icon }
  $s.Description = 'Stream movies to your TV in full quality'
  $s.Save()
}
$programs = [Environment]::GetFolderPath('Programs')
$startup  = [Environment]::GetFolderPath('Startup')
$desktop  = [Environment]::GetFolderPath('Desktop')
Make-Shortcut (Join-Path $programs 'TV Cast.lnk') ''
Make-Shortcut (Join-Path $desktop  'TV Cast.lnk') ''
Make-Shortcut (Join-Path $startup  'TV Cast Engine.lnk') '--background'
Ok 'Start menu + Desktop shortcuts; engine starts at every login'

Say 'Starting TV Cast'
Start-Process $Exe
Ok 'Done. If Windows Firewall asks, tick "Private networks" and click Allow.'
