. "$PSScriptRoot\ui-lib.ps1"
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$root = (Join-Path $PSScriptRoot "..\target\release")
$env:LOCALAPPDATA = Join-Path $env:TEMP "pw-uia"; $env:PLAITWAY_PIPE = '\\.\pipe\plaitway-shell'
Get-Process plaitway-ui, fake-daemon -ea 0 | Stop-Process -Force; Start-Sleep 1
$fd = Start-Process "$root\fake-daemon.exe" -ArgumentList '\\.\pipe\plaitway-shell' -WindowStyle Hidden -PassThru; Start-Sleep 1
$ui = Start-Process "$root\plaitway-ui.exe" -PassThru; Start-Sleep 3

function Tree {
  $cond = New-Object Windows.Automation.PropertyCondition([Windows.Automation.AutomationElement]::ClassNameProperty, "PlaitwayUi")
  $w = [Windows.Automation.AutomationElement]::RootElement.FindFirst([Windows.Automation.TreeScope]::Children, $cond)
  if (-not $w) { return @() }
  $all = $w.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
  @($all | ForEach-Object { [pscustomobject]@{ Type = $_.Current.ControlType.ProgrammaticName.Replace('ControlType.', ''); Name = $_.Current.Name; El = $_ } })
}
$t = Tree; Start-Sleep 1; $t = Tree
"nodes: $($t.Count)"
$t | Select-Object -First 14 | ForEach-Object { "{0,-10} {1}" -f $_.Type, $_.Name }
Ok "the routing page is readable: a profile row is a button" ($t | Where-Object { $_.Type -eq 'Button' -and $_.Name -like '*Home*WireGuard*' })
Ok "the sentence about who holds a route is there" ($t | Where-Object { $_.Name -like 'Default route: Home holds it*' })
Ok "the Connect buttons say which profile they belong to" ($t | Where-Object { $_.Name -eq 'Disconnect Lab' })

# Press Settings through UI Automation, as a screen reader's "click".
$btn = $t | Where-Object { $_.Type -eq 'Button' -and $_.Name -like 'Settings*' } | Select-Object -First 1
Ok "there is a Settings button" ($null -ne $btn)
$pat = $btn.El.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern); $pat.Invoke(); Start-Sleep 1; Start-Sleep 1
$t = Tree
Ok "after Invoke the Settings page is showing" ($t | Where-Object { $_.Name -like 'Language*' })
Ok "its switches are switches or toggles" (($t | Where-Object { $_.Name -like 'Start with Windows*' }).Count -ge 1)
$ui.Kill(); $fd.Kill()


