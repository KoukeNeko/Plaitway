. "$PSScriptRoot\ui-lib.ps1"
$root = Join-Path $PSScriptRoot "..\target\release"
$env:LOCALAPPDATA = Join-Path $env:TEMP "pw-helper-ui"
Get-Process plaitway-ui, fake-daemon -ea 0 | Stop-Process -Force; Start-Sleep 1
# 1. the daemon cannot be reached: the helper card says what to do (this machine's service is only looked at, never pressed)
$env:PLAITWAY_PIPE = '\\.\pipe\plaitway-nobody-listens'
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3
Shot "helper-offline"
$ui.Kill(); Start-Sleep 1
# 2. Settings with a reachable daemon
$env:PLAITWAY_PIPE = '\\.\pipe\plaitway-shell'
$fd = Start-Process "$root\fake-daemon.exe" -ArgumentList '\\.\pipe\plaitway-shell' -WindowStyle Hidden -PassThru; Start-Sleep 1
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3
Click 32 760; Click 100 760; Start-Sleep 1
Shot "helper-settings"
$ui.Kill(); $fd.Kill()
