# Drags a profile of the sidebar to another place with the (sent) mouse and reads the priority order back from the daemon; then clicks
# one without moving and checks that its page opens. Against the fake daemon, never the real one.
. "$PSScriptRoot\ui-lib.ps1"
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$root = (Join-Path $PSScriptRoot "..\target\release")
$pipe = '\\.\pipe\plaitway-sidebar'
$env:LOCALAPPDATA = Join-Path $env:TEMP "pw-sidebar"; $env:PLAITWAY_PIPE = $pipe
Get-Process plaitway-ui, fake-daemon -ea 0 | Stop-Process -Force; Start-Sleep 1
$fd = Start-Process "$root\fake-daemon.exe" -ArgumentList $pipe -WindowStyle Hidden -PassThru; Start-Sleep 1
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3

function Names { (& "$root\pwctl.exe" $pipe list) | ForEach-Object { ($_ -split '\|')[1] } }
# A mouse message with the left button state, as the system sends it (the moves carry MK_LBUTTON while it is down).
function Mouse($m, $w, $lx, $ly) { $s = Scale; $l = ([int]($ly * $s) -shl 16) -bor ([int]($lx * $s) -band 0xffff); [void][UI]::SendMessage((Win), $m, [IntPtr]$w, [IntPtr]$l); Start-Sleep -m 250 }

# Wide layout: the first profile row is 118 below the caption (32), and the rows are 56 apart; x is inside the sidebar.
$x = 100; $first = 32 + 118; $pitch = 56
$before = @(Names)
"before: $($before -join ', ')"
Ok "the fake daemon has at least three profiles" ($before.Count -ge 3)

Mouse 0x200 0 $x $first
Mouse 0x201 1 $x $first
Mouse 0x200 1 $x ($first + 20)
Mouse 0x200 1 $x ($first + $pitch * 2)
Mouse 0x202 0 $x ($first + $pitch * 2)
Start-Sleep 1
$after = @(Names)
"after:  $($after -join ', ')"
Ok "the first profile was dragged to the third place" (($after[2] -eq $before[0]) -and ($after[0] -eq $before[1]) -and ($after[1] -eq $before[2]))

# A press and release in place opens the profile.
function Tree {
  $c = New-Object Windows.Automation.PropertyCondition([Windows.Automation.AutomationElement]::ClassNameProperty, "PlaitwayUi")
  $w = [Windows.Automation.AutomationElement]::RootElement.FindFirst([Windows.Automation.TreeScope]::Children, $c)
  if (-not $w) { return @() }
  @($w.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition) | ForEach-Object { $_.Current.Name })
}
Ok "no profile page is open before the click" (-not (Tree | Where-Object { $_ -eq 'Overview' }))
Mouse 0x200 0 $x $first
Mouse 0x201 1 $x $first
Mouse 0x202 0 $x $first
Start-Sleep 1
Ok "a click without moving opens the profile (its tabs are there)" ([bool](Tree | Where-Object { $_ -eq 'Overview' }))
$final = @(Names)
Ok "a click does not change the order" (($final -join ',') -eq ($after -join ','))

$ui.Kill(); $fd.Kill()
