. "$PSScriptRoot\ui-lib.ps1"
$root = (Join-Path $PSScriptRoot "..\target\release")
$scratch = Join-Path $env:TEMP "pw-lang"; if (Test-Path $scratch) { [IO.Directory]::Delete($scratch, $true) }; New-Item -ItemType Directory -Force $scratch | Out-Null
$env:LOCALAPPDATA = $scratch; $env:PLAITWAY_PIPE = '\\.\pipe\plaitway-shell'
Get-Process plaitway-ui, fake-daemon -ea 0 | Stop-Process -Force; Start-Sleep 1
$fd = Start-Process "$root\fake-daemon.exe" -ArgumentList '\\.\pipe\plaitway-shell' -WindowStyle Hidden -PassThru; Start-Sleep 1
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3
"system locale: $((Get-Culture).Name) / UI $((Get-UICulture).Name)"
Shot "lang-1-start"
Click 60 760
Click 400 167; Start-Sleep 1
Shot "lang-2-first-press"
Click 400 167; Start-Sleep 1
Shot "lang-3-second-press"
"saved: " + (Get-Content "$scratch\Plaitway\settings.json" -Raw)
$ui.Kill(); $fd.Kill()


