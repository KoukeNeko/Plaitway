. "$PSScriptRoot\ui-lib.ps1"
$exe = Join-Path $env:TEMP "pw-untrusted-copy\plaitway-ui.exe"
$env:LOCALAPPDATA = Join-Path $env:TEMP "pw-helper-ui2"; $env:PLAITWAY_PIPE = '\\.\pipe\plaitway-nobody-listens'
Get-Process plaitway-ui -ea 0 | Stop-Process -Force; Start-Sleep 1
$ui = Start-Process $exe -PassThru; Start-Sleep 3
Shot "helper-untrusted"
$ui.Kill()
