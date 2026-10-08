<#
.SYNOPSIS
Runs plaitwayd in this elevated shell with the real engines, on a scratch pipe
and a scratch state directory, and checks what it left behind when it ends.

.DESCRIPTION
For trying the Windows engines by hand. The daemon runs in the foreground of
this window: Ctrl+C stops it the way the service manager's Stop does, and the
Reconciler then removes the routes and DNS rules it installed. When the daemon
has ended, the script lists every Plaitway-* adapter, every Plaitway-* DNS rule
(NRPT) and every route that is there and was not there before, and the journal
records that were never marked removed. It exits with 1 when anything is left.

It does not touch the default pipe (\\.\pipe\plaitway), %ProgramData%\Plaitway or
the PlaitwayHelper service. It refuses to start while that service or another
plaitwayd runs, because a daemon removes every DNS rule and every adapter that
carries the Plaitway name when it starts, and those would be the service's.

The daemon answers on \\.\pipe\plaitway-dev-<random>. Point the command line
client at it with -socket or with $env:PLAITWAY_SOCKET (the script prints both).
Listing and connecting work from a normal shell on the console; importing or
editing profiles needs the shell to be elevated.

wintun.dll has to be next to plaitwayd.exe for the WireGuard engine; the script
says when it is not. OpenVPN is the installation the daemon finds by itself
(C:\Program Files\OpenVPN) unless -OpenVPN names another openvpn.exe.

A scratch directory with leftovers, or with -KeepScratch, stays; start the
script again with -ScratchDirectory on it to let the daemon replay the journal
(a daemon that was killed leaves what it installed to the next start).

After Ctrl+C PowerShell ends the script with an exit code of its own, so read
the report; when the daemon ended by itself, the exit code is the result.

.PARAMETER Daemon
plaitwayd.exe to run. Default: bin\plaitwayd.exe of this repository.

.PARAMETER Client
plaitway.exe, only to print its command lines. Default: bin\plaitway.exe.

.PARAMETER ScratchDirectory
Holds state\ (profiles and journal), run\ and plaitwayd.log. Default: a new
folder below %TEMP%.

.PARAMETER OpenVPN
An openvpn.exe other than the standard installation.

.PARAMETER LogLevel
debug, info, warn or error. Default: info.

.PARAMETER StopTimeoutSeconds
How long to wait for the daemon to stop after Ctrl+C before it is ended by
force. The daemon's own budget is 17 seconds. Default: 25.

.PARAMETER KeepScratch
Keeps the scratch directory when nothing was left behind, too.

.PARAMETER AllowUnelevated
Runs in a shell that is not elevated, where the daemon can read the network but
neither create adapters nor change routes. For checking this script itself.

.EXAMPLE
# From an elevated PowerShell in the repository root:
.\packaging\windows\dev\Run-DaemonElevated.ps1
#>
[CmdletBinding()]
param(
    [string] $Daemon = (Join-Path $PSScriptRoot '..\..\..\bin\plaitwayd.exe'),
    [string] $Client = (Join-Path $PSScriptRoot '..\..\..\bin\plaitway.exe'),
    [string] $ScratchDirectory = (Join-Path ([IO.Path]::GetTempPath()) ('plaitway-dev-{0:yyyyMMdd-HHmmss}' -f (Get-Date))),
    [string] $OpenVPN,
    [ValidateSet('debug', 'info', 'warn', 'error')]
    [string] $LogLevel = 'info',
    [int] $StopTimeoutSeconds = 25,
    [switch] $KeepScratch,
    [switch] $AllowUnelevated
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ServiceName = 'PlaitwayHelper'
$DaemonProcessName = 'plaitwayd'
$AdapterNamePattern = 'Plaitway-*'
$DnsRuleKeyPrefix = 'Plaitway-'
$DnsRulesPath = 'HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig'
$PipePrefix = '\\.\pipe\plaitway-dev-'
$WintunFileName = 'wintun.dll'
$StopPollMilliseconds = 500
$ResolvedState = 'removed'
$ExitClean = 0
$ExitLeftovers = 1

function Test-Elevated {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-NothingElseRuns {
    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($service -and $service.Status -ne 'Stopped') {
        throw "The $ServiceName service is $($service.Status). A second daemon would remove its DNS rules and adapters at start. Stop the service first (plaitwayd.exe stop)."
    }
    # A daemon on the fake backend (-fake) touches nothing, so it is no reason to refuse.
    $others = @(Get-CimInstance -ClassName Win32_Process -Filter "Name = '$DaemonProcessName.exe'" |
            Where-Object { $_.CommandLine -notmatch '(^|\s)-fake(\s|$)' })
    if ($others.Count -gt 0) {
        throw "plaitwayd already runs (process id $($others.ProcessId -join ', ')). Stop it first: a second daemon would remove its DNS rules and adapters at start."
    }
}

function Resolve-ExistingFile([string] $Path, [string] $Name) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Name not found: $Path. Build it with: go build -o bin\$Name .\cmd\$([IO.Path]::GetFileNameWithoutExtension($Name))"
    }
    return (Resolve-Path -LiteralPath $Path).ProviderPath
}

# What the daemon may have put on the machine, in a form that two snapshots can be compared in.
function Get-HostFootprint {
    $adapters = @(Get-NetAdapter -Name $AdapterNamePattern -IncludeHidden -ErrorAction SilentlyContinue |
            ForEach-Object { $_.InterfaceAlias })
    $dnsRules = @()
    if (Test-Path -LiteralPath $DnsRulesPath) {
        $dnsRules = @(Get-ChildItem -LiteralPath $DnsRulesPath |
                Where-Object { $_.PSChildName.StartsWith($DnsRuleKeyPrefix) } |
                ForEach-Object { $_.PSChildName })
    }
    $routes = @(Get-NetRoute -PolicyStore ActiveStore -ErrorAction SilentlyContinue |
            ForEach-Object { '{0} via {1} on interface {2}' -f $_.DestinationPrefix, $_.NextHop, $_.InterfaceIndex })
    return [pscustomobject]@{ Adapters = $adapters; DnsRules = $dnsRules; Routes = $routes }
}

function Get-NewItems([string[]] $Before, [string[]] $After) {
    return @($After | Where-Object { $Before -notcontains $_ })
}

# The journal records that the last line about them did not mark removed. The
# identity is the one internal/reconciler builds: kind and key, and for a
# route with an interface index also that index and the next hop.
function Get-UnresolvedJournalRecords([string] $JournalPath) {
    if (-not (Test-Path -LiteralPath $JournalPath)) { return @() }
    $latest = [ordered]@{}
    foreach ($line in Get-Content -LiteralPath $JournalPath) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $record = $line | ConvertFrom-Json } catch { continue }
        $identity = '{0} {1}' -f $record.kind, $record.key
        if ($record.PSObject.Properties['ifindex'] -and $record.ifindex) {
            $gateway = ''
            if ($record.PSObject.Properties['gateway']) { $gateway = $record.gateway }
            $identity = '{0} if{1} via {2}' -f $identity, $record.ifindex, $gateway
        }
        $latest[$identity] = $record.state
    }
    return @($latest.Keys | Where-Object { $latest[$_] -ne $ResolvedState })
}

function ConvertTo-ArgumentString([string[]] $Arguments) {
    return ($Arguments | ForEach-Object { if ($_ -match '[\s"]') { '"' + ($_ -replace '"', '\"') + '"' } else { $_ } }) -join ' '
}

# Ctrl+C reaches the daemon too (it shares this console), so by now it is
# stopping; it gets its own time to remove what it installed. Returns whether it
# ended by itself.
function Wait-DaemonStop([System.Diagnostics.Process] $Process, [int] $TimeoutSeconds) {
    if ($Process.HasExited) { return $true }
    Write-Host ''
    Write-Host "Waiting up to $TimeoutSeconds s for the daemon to remove its routes and DNS rules..."
    if ($Process.WaitForExit($TimeoutSeconds * 1000)) { return $true }
    Write-Warning "The daemon did not stop within $TimeoutSeconds s and is ended by force: its cleanup may not have run."
    Stop-Process -Id $Process.Id -Force
    $Process.WaitForExit()
    return $false
}

function Write-Findings([string] $Title, [string[]] $Items, [string] $Verdict, [ConsoleColor] $Color) {
    if ($Items.Count -eq 0) {
        Write-Host ('  {0,-34} none' -f $Title)
        return
    }
    Write-Host ('  {0,-34} {1} {2}' -f $Title, $Items.Count, $Verdict) -ForegroundColor $Color
    $Items | ForEach-Object { Write-Host "    $_" -ForegroundColor $Color }
}

# Ends the run: waits for the daemon, compares the machine with how it was and
# says so. It runs from a finally block, because Ctrl+C ends the script after it.
function Complete-Run($Run) {
    if (-not $Run.Process) { return $ExitLeftovers }
    $stoppedByItself = Wait-DaemonStop $Run.Process $StopTimeoutSeconds
    $after = Get-HostFootprint
    $leftAdapters = @(Get-NewItems $Run.Before.Adapters $after.Adapters)
    $leftDnsRules = @(Get-NewItems $Run.Before.DnsRules $after.DnsRules)
    $leftRoutes = @(Get-NewItems $Run.Before.Routes $after.Routes)
    $unresolved = @(Get-UnresolvedJournalRecords $Run.Journal)
    $exitCode = $Run.Process.ExitCode

    Write-Host ''
    Write-Host 'After the daemon:'
    Write-Host ('  {0,-34} {1}' -f 'Exit code', $exitCode)
    Write-Findings 'Plaitway-* adapters' $leftAdapters 'LEFT' Red
    Write-Findings 'Plaitway-* DNS rules' $leftDnsRules 'LEFT' Red
    Write-Findings 'Routes that were not there before' $leftRoutes 'LEFT' Red
    Write-Findings 'Journal records not removed' $unresolved 'LEFT' Red
    if (Test-Path -LiteralPath $Run.LogFile) {
        $errors = @(Select-String -LiteralPath $Run.LogFile -Pattern 'level=ERROR' | ForEach-Object { $_.Line })
        Write-Findings 'Errors in the log' $errors 'in' Yellow
    }

    $leftover = $leftAdapters.Count + $leftDnsRules.Count + $leftRoutes.Count + $unresolved.Count
    if ($exitCode -eq 0 -and $stoppedByItself -and $leftover -eq 0) {
        Write-Host ''
        Write-Host 'The daemon stopped with exit code 0 and removed everything it installed.' -ForegroundColor Green
        if (-not $KeepScratch) {
            Remove-Item -LiteralPath $Run.Scratch -Recurse -Force
            Write-Host "Removed $($Run.Scratch)."
        }
        return $ExitClean
    }
    Write-Host ''
    Write-Host 'Not clean.' -ForegroundColor Red
    if (Test-Path -LiteralPath $Run.Scratch) {
        Write-Host "The scratch directory stays: $($Run.Scratch)"
        Write-Host 'To let a new daemon replay the journal and remove what is left:'
        Write-Host "  .\packaging\windows\dev\Run-DaemonElevated.ps1 -ScratchDirectory '$($Run.Scratch)'"
    }
    return $ExitLeftovers
}

if (-not (Test-Elevated)) {
    if (-not $AllowUnelevated) { throw 'This needs administrator rights: run it from an elevated PowerShell.' }
    Write-Warning 'Not elevated: the daemon can read the network but not create adapters or change routes.'
}
Assert-NothingElseRuns
$Daemon = Resolve-ExistingFile $Daemon 'plaitwayd.exe'
$Client = Resolve-ExistingFile $Client 'plaitway.exe'

$pipe = $PipePrefix + ([guid]::NewGuid().ToString('N').Substring(0, 8))
if (Test-Path -LiteralPath $pipe) { throw "The pipe $pipe exists already." }
$stateDir = Join-Path $ScratchDirectory 'state'
$runDir = Join-Path $ScratchDirectory 'run'
$logFile = Join-Path $ScratchDirectory 'plaitwayd.log'

$daemonArguments = @('-socket', $pipe, '-state-dir', $stateDir, '-run-dir', $runDir, '-log-file', $logFile, '-log-level', $LogLevel)
if ($OpenVPN) { $daemonArguments += @('-openvpn', $OpenVPN) }

$wintunFolder = Split-Path $Daemon -Parent
if (-not (Test-Path -LiteralPath (Join-Path $wintunFolder $WintunFileName))) {
    Write-Warning ("$WintunFileName is not next to plaitwayd.exe, so the WireGuard engine will say it is unavailable. " +
        "Fetch the checked copy with: powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory $wintunFolder")
}

Write-Host "Daemon    $Daemon"
Write-Host "Pipe      $pipe"
Write-Host "Scratch   $ScratchDirectory"
Write-Host ''
Write-Host 'In another window, to talk to this daemon:'
Write-Host "  `$env:PLAITWAY_SOCKET = '$pipe'"
Write-Host "  & '$Client' diagnostics"
Write-Host "  & '$Client' list"
Write-Host "  & '$Client' import <file>        (needs an elevated shell)"
Write-Host ''
Write-Host 'Ctrl+C stops the daemon and checks what it left behind.'
Write-Host ''

$run = [pscustomobject]@{
    Process = $null
    Before  = Get-HostFootprint
    Journal = Join-Path $stateDir 'journal'
    LogFile = $logFile
    Scratch = $ScratchDirectory
}
try {
    $run.Process = Start-Process -FilePath $Daemon -ArgumentList (ConvertTo-ArgumentString $daemonArguments) -NoNewWindow -PassThru
    while (-not $run.Process.WaitForExit($StopPollMilliseconds)) { }
}
finally {
    # Ctrl+C ends the script after this block, so the exit code is set here.
    exit (Complete-Run $run)
}
