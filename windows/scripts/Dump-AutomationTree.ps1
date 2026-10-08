# Writes the UI Automation tree of a running window to a text file, and says which controls that need a name have none.
#
#   pwsh windows\scripts\Dump-AutomationTree.ps1 -ProcessName Plaitway -OutputFile tree.txt
#
# It only reads: it changes nothing in the app. The same tree is what a screen reader walks, so a control without a name
# there is a control that is read as "button" and nothing more. The exit code is 1 when there is one.

[CmdletBinding()]
param(
    [string] $ProcessName = 'Plaitway',
    [int] $ProcessId = 0,
    [Parameter(Mandatory)] [string] $OutputFile
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'UiAutomationHelpers.ps1')

$process = if ($ProcessId -gt 0) { Get-Process -Id $ProcessId } else { Get-Process -Name $ProcessName | Where-Object { $_.MainWindowHandle -ne [IntPtr]::Zero } | Select-Object -First 1 }
if (-not $process) { throw "no running window of $ProcessName" }

$root = [System.Windows.Automation.AutomationElement]::FromHandle($process.MainWindowHandle)
$tree = Get-AutomationTreeText $root
$unnamed = @(Get-UnnamedControls $root)
$output = @($tree) + @('', "controls that need a name and have none: $($unnamed.Count)") + @($unnamed | ForEach-Object { "  $_" })
New-Item -ItemType Directory -Force (Split-Path -Parent ([IO.Path]::GetFullPath($OutputFile))) | Out-Null
[IO.File]::WriteAllLines([IO.Path]::GetFullPath($OutputFile), $output, (New-Object Text.UTF8Encoding $false))
Write-Host "wrote $OutputFile ($($tree.Count) controls, $($unnamed.Count) without a name)"
if ($unnamed.Count -gt 0) { exit 1 }
