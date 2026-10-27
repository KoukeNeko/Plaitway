<#
.SYNOPSIS
Writes the notices of the Rust crates the app is built from: name, version, licence and the licence texts as the
crates ship them. The texts are copied from the sources Cargo downloaded, never typed.

.PARAMETER Output
The file to write (Markdown).

.PARAMETER Package
The package whose dependencies are listed; the default is the app.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $Output,
    [string] $Package = 'plaitway-ui'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
try {
    # Normal dependencies for Windows only: what is linked into the program. "{p}" is "name vX.Y.Z (path)", "{l}" the licence expression.
    $lines = cargo tree -p $Package --edges normal --target x86_64-pc-windows-msvc --prefix none --format '{p}|{l}|{r}' 2>&1
    if ($LASTEXITCODE -ne 0) { throw "cargo tree failed: $lines" }
}
finally { Pop-Location }

$crates = @{}
foreach ($line in $lines) {
    $parts = "$line".Split('|')
    if ($parts.Count -lt 3 -or $parts[0] -notmatch '^(?<name>\S+) v(?<version>\S+)(?<rest>.*)$') { continue }
    # Our own crates (they have a path) are not third party.
    if ($Matches.rest -match '\(') { continue }
    $crates["$($Matches.name) $($Matches.version)"] = [pscustomobject]@{ Name = $Matches.name; Version = $Matches.version; License = $parts[1]; Repository = $parts[2] }
}
if ($crates.Count -eq 0) { throw 'cargo tree listed no third-party crate.' }

$registry = Join-Path $env:USERPROFILE '.cargo\registry\src'
$out = New-Object System.Text.StringBuilder
[void]$out.AppendLine('# Third-party notices: the Rust crates of the Windows app')
[void]$out.AppendLine()
[void]$out.AppendLine("These $($crates.Count) crates are linked into the program. Each licence text is the one the crate ships.")
[void]$out.AppendLine()
$missing = @()
foreach ($crate in $crates.Values | Sort-Object Name, Version) {
    [void]$out.AppendLine("## $($crate.Name) $($crate.Version)")
    [void]$out.AppendLine()
    [void]$out.AppendLine("- Licence: $($crate.License)")
    if ($crate.Repository) { [void]$out.AppendLine("- Source: $($crate.Repository)") }
    [void]$out.AppendLine()
    $dir = Get-ChildItem $registry -Directory | ForEach-Object { Join-Path $_.FullName "$($crate.Name)-$($crate.Version)" } | Where-Object { Test-Path $_ } | Select-Object -First 1
    $texts = if ($dir) { @(Get-ChildItem $dir -File | Where-Object { $_.Name -match '^(LICEN[CS]E|COPYING|NOTICE|UNLICENSE)' }) } else { @() }
    if (-not $texts) { $missing += "$($crate.Name) $($crate.Version)"; [void]$out.AppendLine('(the crate ships no licence file; see its source)'); [void]$out.AppendLine(); continue }
    foreach ($t in $texts) {
        [void]$out.AppendLine("### $($t.Name)")
        [void]$out.AppendLine()
        [void]$out.AppendLine('```text')
        [void]$out.AppendLine((Get-Content -LiteralPath $t.FullName -Raw).TrimEnd())
        [void]$out.AppendLine('```')
        [void]$out.AppendLine()
    }
}
[IO.File]::WriteAllText($Output, $out.ToString(), (New-Object System.Text.UTF8Encoding $false))
Write-Host "$($crates.Count) crates, $($missing.Count) without a licence file in their source: $($missing -join ', ')"
