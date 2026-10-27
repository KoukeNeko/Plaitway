<#
.SYNOPSIS
Builds the Rust app and lays it out the way the installer package takes it: app\Plaitway.exe, and the notices of
its crates in licenses\. The package (windows\installer\Package.wxs) takes app\Plaitway.exe and whatever else is in
app\, so nothing of the package has to change for a one-file app.

.PARAMETER Output
The folder to fill; it is emptied first.
#>
[CmdletBinding()]
param([Parameter(Mandatory)] [string] $Output)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = Join-Path $PSScriptRoot '..'
Push-Location $root
try {
    cargo build --release -p plaitway-ui
    if ($LASTEXITCODE -ne 0) { throw 'cargo build failed' }
}
finally { Pop-Location }

$Output = [IO.Path]::GetFullPath($Output)
if (Test-Path -LiteralPath $Output) { [IO.Directory]::Delete($Output, $true) }
[void](New-Item -ItemType Directory -Force -Path (Join-Path $Output 'app'), (Join-Path $Output 'licenses'))
Copy-Item -LiteralPath (Join-Path $root 'target\release\plaitway-ui.exe') -Destination (Join-Path $Output 'app\Plaitway.exe')
& (Join-Path $PSScriptRoot 'notices.ps1') -Output (Join-Path $Output 'licenses\THIRD_PARTY_NOTICES_RUST.md')
Get-ChildItem -LiteralPath $Output -Recurse -File | ForEach-Object { '{0,10}  {1}' -f $_.Length, $_.FullName.Substring($Output.Length + 1) }
