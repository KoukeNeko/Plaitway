Add-Type @'
using System; using System.Runtime.InteropServices;
public static class U {
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string c, string t);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out R r);
  [DllImport("user32.dll")] public static extern bool MoveWindow(IntPtr h, int x, int y, int w, int hh, bool r);
  [DllImport("user32.dll")] public static extern IntPtr SendMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
  [DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr h);
  [StructLayout(LayoutKind.Sequential)] public struct R { public int L, T, Rt, B; }
}
'@
[void][U]::SetProcessDPIAware()
$root = (Join-Path $PSScriptRoot "..\target\release")
$scratch = Join-Path $env:TEMP "pw-shell-test"
if (Test-Path $scratch) { Get-ChildItem $scratch | ForEach-Object { $_.Delete() 2>$null; if ($_.PSIsContainer) { [IO.Directory]::Delete($_.FullName, $true) } } }
New-Item -ItemType Directory -Force $scratch | Out-Null
$env:LOCALAPPDATA = $scratch; $env:PLAITWAY_PIPE = '\\.\pipe\plaitway-shell'; $env:PLAITWAY_RUN_VALUE = 'PlaitwayTest'
Get-Process plaitway-ui, fake-daemon -ea 0 | Stop-Process -Force
$fd = Start-Process "$root\fake-daemon.exe" -ArgumentList '\\.\pipe\plaitway-shell' -WindowStyle Hidden -PassThru; Start-Sleep 1
function Win { [U]::FindWindow("PlaitwayUi", "Plaitway") }
function Ok($name, $cond) { "{0,-62} {1}" -f $name, $(if ($cond) { "ok" } else { "FAILED" }) }
function Start-Ui($more) { if ($more) { Start-Process "$root\plaitway-ui.exe" -ArgumentList $more -PassThru } else { Start-Process "$root\plaitway-ui.exe" -PassThru } }
$ui = Start-Ui $null; Start-Sleep 3
$h = Win
Ok "the window shows at start" ([U]::IsWindowVisible($h))
$second = Start-Ui $null; $second.WaitForExit(5000) | Out-Null
Ok "a second copy exits at once" ($second.HasExited)
Ok "and the first keeps running" (-not $ui.HasExited)
[void][U]::SendMessage($h, 0x10, [IntPtr]0, [IntPtr]0); Start-Sleep 1
Ok "closing hides the window to the tray" ((-not [U]::IsWindowVisible($h)) -and -not $ui.HasExited)
Ok "the placement was saved" (Test-Path "$scratch\Plaitway\ui.json")
$second = Start-Ui $null; $second.WaitForExit(5000) | Out-Null; Start-Sleep 1
Ok "a second copy brings the hidden window back" ([U]::IsWindowVisible($h))
[void][U]::MoveWindow($h, 220, 140, 1200, 760, $true); Start-Sleep 1
[void][U]::SendMessage($h, 0x10, [IntPtr]0, [IntPtr]0); Start-Sleep 1
$saved = Get-Content "$scratch\Plaitway\ui.json" -Raw | ConvertFrom-Json
"saved window: x=$($saved.window.x) y=$($saved.window.y) w=$($saved.window.w) h=$($saved.window.h) close_to_tray=$($saved.close_to_tray)"
Ok "the saved size is what the window had" (($saved.window.w -eq 1200) -and ($saved.window.h -eq 760))
$ui.Kill(); $ui.WaitForExit()
$ui = Start-Ui $null; Start-Sleep 3; $h = Win
$r = New-Object U+R; [void][U]::GetWindowRect($h, [ref]$r)
"restored rect: $($r.L),$($r.T) $($r.Rt - $r.L)x$($r.B - $r.T)"
Ok "the window comes back where it was" (($r.L -eq 220) -and ($r.T -eq 140) -and (($r.Rt - $r.L) -eq 1200))
$s = [U]::GetDpiForWindow($h) / 96.0
function Msg($m, $lx, $ly) { $l = ([int]($ly*$s) -shl 16) -bor ([int]($lx*$s) -band 0xffff); [void][U]::SendMessage($h, $m, [IntPtr]0, [IntPtr]$l); Start-Sleep -m 400 }
function Click($x, $y) { Msg 0x200 $x $y; Msg 0x201 $x $y; Msg 0x202 $x $y }
$run = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
Ok "start with Windows is off to begin with" ($null -eq (Get-ItemProperty $run -Name PlaitwayTest -ea 0).PlaitwayTest)
# window is 1200x760 now; Settings sits 46 px above the bottom edge of the client area
$ch = ($r.B - $r.T) / $s - 8
Click 32 ($ch - 54 + 20); Start-Sleep 1
Click 400 227; Start-Sleep 1
$v = (Get-ItemProperty $run -Name PlaitwayTest -ea 0).PlaitwayTest
"run value: $v"
Ok "its switch writes the Run key, hidden" ($v -match 'plaitway-ui.exe" --hidden$')
Click 400 227; Start-Sleep 1
Ok "and turning it off removes the key" ($null -eq (Get-ItemProperty $run -Name PlaitwayTest -ea 0).PlaitwayTest)
Click 400 273; Start-Sleep 1
$saved = Get-Content "$scratch\Plaitway\ui.json" -Raw | ConvertFrom-Json
Ok "the tray switch is saved" ($saved.close_to_tray -eq $false)
[void][U]::SendMessage($h, 0x10, [IntPtr]0, [IntPtr]0); Start-Sleep 1
Ok "with it off, closing quits" ($ui.HasExited)
Start-Sleep 2; Get-Process plaitway-ui -ea 0 | Select Id, StartTime | Out-String | Write-Host; $ui = Start-Ui '--hidden'; Start-Sleep 3
"hidden: code=$($ui.ExitCode) procs=$((Get-Process plaitway-ui -ea 0 | Measure-Object).Count) exited=$($ui.HasExited) hwnd=$(Win) visible=$([U]::IsWindowVisible((Win)))"; Ok "--hidden starts without a window" ((-not [U]::IsWindowVisible((Win))) -and -not $ui.HasExited)
$ui.Kill(); $fd.Kill()
Remove-ItemProperty $run -Name PlaitwayTest -ea 0






