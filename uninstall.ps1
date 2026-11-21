# Removes TV Cast from Windows.   .\uninstall.ps1        (keeps your TVs & settings)
#                                 .\uninstall.ps1 -All   (also deletes settings)
param([switch]$All)
Get-Process -Name 'TVCast' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500
$Dir = Join-Path $env:LOCALAPPDATA 'TVCast'
Remove-Item (Join-Path $Dir 'TVCast.exe'), (Join-Path $Dir 'TVCast.ico') -Force -ErrorAction SilentlyContinue
foreach ($p in @(
  (Join-Path ([Environment]::GetFolderPath('Programs')) 'TV Cast.lnk'),
  (Join-Path ([Environment]::GetFolderPath('Desktop'))  'TV Cast.lnk'),
  (Join-Path ([Environment]::GetFolderPath('Startup'))  'TV Cast Engine.lnk'))) {
  Remove-Item $p -Force -ErrorAction SilentlyContinue
}
Write-Host ' ✓ TV Cast removed' -ForegroundColor Green
if ($All) {
  Remove-Item (Join-Path $env:APPDATA 'TVCast') -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item $Dir -Recurse -Force -ErrorAction SilentlyContinue
  Write-Host ' ✓ Settings deleted' -ForegroundColor Green
} else { Write-Host 'Your TVs and settings were kept, so reinstalling picks up where you left off.' }
