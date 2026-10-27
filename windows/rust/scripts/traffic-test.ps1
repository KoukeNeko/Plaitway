. "$PSScriptRoot\ui-lib.ps1"
$root = Join-Path $PSScriptRoot "..\target\release"
$pipe = '\\.\pipe\plaitway-real'
$imp = Join-Path $env:TEMP "pw-import"
& "$root\pwctl.exe" $pipe clear
$env:LOCALAPPDATA = Join-Path $env:TEMP "pw-traffic"; $env:PLAITWAY_PIPE = $pipe; $env:PLAITWAY_IMPORT_FILE = "$imp\Cafe.conf"
Get-Process plaitway-ui -ea 0 | Stop-Process -Force; Start-Sleep 1
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3
Click 340 162; Start-Sleep 1
Click 100 150; Click 1200 83; Start-Sleep 12
Shot "traffic"
$ui.Kill(); & "$root\pwctl.exe" $pipe clear
