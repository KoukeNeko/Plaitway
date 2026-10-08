<#
.SYNOPSIS
Runs the Windows tests that need an elevated shell, group by group, and checks
after each group what they left on the machine.

.DESCRIPTION
The tests are the ones tagged rootintegration. Which tests there are, in which
group, what each changes and how to undo it is written once, in
Docs\windows-elevated-tests.md; this script reads the Groups table and the
Tests table of that page and runs what they say. Add a row there and the script
runs it; -CheckTable fails when a row names a test that does not exist.

The default is the groups marked "yes" in that table (Routes and Dns): scratch
routes on the loopback pseudo-interface and NRPT rules for .invalid names. The
other groups create adapters (and may install the wintun or tap-windows6
driver), start the real daemon, or create and remove a real service; they run
only when -Include names them.

Before a group the script prints what it changes and asks for the group's name
to be typed (-Yes skips that, for a virtual machine). It takes a snapshot of the
machine before, runs the group's tests from binaries it built once, takes
another snapshot, and compares them. Anything named Plaitway-*, anything in the
documentation address ranges, a changed loopback or a changed PlaitwayHelper
service, a firewall rule that names a test binary and a socket on an address
that is not loopback are reported as left behind, with the command that removes
them by hand, and the run stops. Other differences are listed as changes.

It never touches the real PlaitwayHelper service or %ProgramData%\Plaitway: it
only reads the state of the service, and the Service group works on a service
named PlaitwayHelperTest.

The log of the run, with the logs of the guard processes of the tests, is
written to <OutputDirectory>\logs. It names routes, adapters and services of the
machine; read it before sending it.

Exit codes: 0 every selected group passed and left nothing; 1 a test failed or
was skipped, or a group was declined; 2 the script refused to start; 3 something
is left on the machine.

.PARAMETER Include
Groups to run, from the Groups table. Default: the groups marked "yes" there.

.PARAMETER Yes
Does not ask for the typed confirmation of each group. For a virtual machine.

.PARAMETER TestMachine
Needed for the Service group, which creates and removes a real service.

.PARAMETER CatchAll
Needed for the Dns test of the "." rule: for a moment every name lookup of the
PC goes to a throw-away resolver.

.PARAMETER IAcceptOpenVpnInterruption
Lets the OpenVpn group run while an openvpn.exe that is not the tests' runs. The
script then compares that process and its TAP adapters before and after.

.PARAMETER WintunDll
A signed wintun.dll other than bin\wintun\wintun.dll of the repository.

.PARAMETER OutputDirectory
Where the test binaries, the socket watcher and the logs go. Default: a fixed
folder below %TEMP%, so that a binary that listens on the wrong address raises
one firewall prompt for one path and not a new one per run.

.PARAMETER TestTimeoutMinutes
The timeout given to each test binary. Default: 20.

.PARAMETER SnapshotOnly
Takes the snapshot twice and compares the two, to show what the comparison
reports on this machine when nothing runs. Changes nothing, needs no elevation.

.PARAMETER CheckTable
Builds the test binaries and checks that every row of the tables names a test
that exists and a switch the script knows. Changes nothing, needs no elevation.

.PARAMETER SelfTest
Checks the parsing, the verdicts and the comparison rules of this script on
made-up data. Touches nothing.

.PARAMETER AllowUnelevated
Runs in a shell that is not elevated. Every test checks for elevation before it
changes anything and fails or skips, so this only exercises the build, the
snapshots, the verdicts and the report of this script. It cannot show whether a
test passes.

.PARAMETER WhatIf
Prints the plan and the state of every precondition and stops. Needs no
elevation.

.EXAMPLE
.\packaging\windows\dev\Run-ElevatedTests.ps1 -WhatIf

.EXAMPLE
# From an elevated PowerShell in the repository root:
.\packaging\windows\dev\Run-ElevatedTests.ps1

.EXAMPLE
# On a test machine:
.\packaging\windows\dev\Run-ElevatedTests.ps1 -Yes -TestMachine -Include Routes,Dns,Wintun,OpenVpn,Daemon,Service
#>
#Requires -Version 5.1
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string[]] $Include,
    [switch] $Yes,
    [switch] $TestMachine,
    [switch] $CatchAll,
    [switch] $IAcceptOpenVpnInterruption,
    [string] $WintunDll,
    [string] $OutputDirectory = (Join-Path ([IO.Path]::GetTempPath()) 'plaitway-elevated-tests'),
    [int] $TestTimeoutMinutes = 20,
    [switch] $SnapshotOnly,
    [switch] $CheckTable,
    [switch] $SelfTest,
    [switch] $AllowUnelevated
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# --- Constants ----------------------------------------------------------------------------------------------------

$ExitPassed = 0
$ExitIncomplete = 1
$ExitRefused = 2
$ExitLeftovers = 3

$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..')).ProviderPath
$TablePath = Join-Path $RepositoryRoot 'Docs\windows-elevated-tests.md'
$WatcherPackage = './scripts/windows/loopbackwatch'
$DefaultWintunDll = Join-Path $RepositoryRoot 'bin\wintun\wintun.dll'
$OpenVpnProgram = Join-Path $env:ProgramFiles 'OpenVPN\bin\openvpn.exe'
$OpenVpnTapctl = Join-Path $env:ProgramFiles 'OpenVPN\bin\tapctl.exe'

$RealServiceName = 'PlaitwayHelper'
$TestServiceName = 'PlaitwayHelperTest'
$ScratchDirectoryFilter = 'PlaitwayHelperTest-*'
$ScratchAdapterPrefix = 'Plaitway-test-'
$ProductMarker = 'Plaitway-'
$LoopbackAlias = 'Loopback Pseudo-Interface 1'
$DnsPort = 53

$DnsRulesPath = 'HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig'
$PolicyDnsRulesPath = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig'
$FirewallRulesPath = 'HKLM:\SYSTEM\CurrentControlSet\Services\SharedAccess\Parameters\FirewallPolicy\FirewallRules'
$DriverStorePath = Join-Path $env:SystemRoot 'System32\DriverStore\FileRepository'

# The ranges the tests use. 2001:db8::/32 is matched by text, because Windows PowerShell 5.1 has no IP network type.
$ScratchIPv4Ranges = @('192.0.2.0/24', '198.51.100.0/24', '203.0.113.0/24')
$ScratchIPv6Start = '2001:db8:'
$TestBinarySuffix = '.test.exe'
$TestBinaryTags = 'rootintegration'
$OwnerOpenVpnPattern = 'OpenVPN|TAP-Windows'
$FindingPattern = 'DIAGNOSTIC:|picked up after|left behind ='
$GuardLogFilter = 'plaitway-rootguard-*.log'
$GuardActedPattern = 'recovered:|RECOVERY FAILED'

$SeverityLeftover = 'Leftover'
$SeverityChanged = 'Changed'
$SeverityNote = 'Note'

$VerdictPassed = 'Passed'
$VerdictFailed = 'Failed'
$VerdictSkipped = 'Skipped'
$VerdictNotRun = 'NotRun'

# --- Output -------------------------------------------------------------------------------------------------------

$script:LogPath = $null

function Write-Line {
    param([string] $Text = '', [string] $Color = '')
    if ($Color) { Write-Host $Text -ForegroundColor $Color } else { Write-Host $Text }
    if ($script:LogPath) { [IO.File]::AppendAllText($script:LogPath, $Text + [Environment]::NewLine) }
}

function Write-Heading([string] $Text) {
    Write-Line ''
    Write-Line ('== ' + $Text) 'Cyan'
}

# --- The tables ---------------------------------------------------------------------------------------------------

function Split-TableRow([string] $Line) {
    $inner = $Line.Trim()
    $inner = $inner.Substring(1, $inner.Length - 2)
    return @([regex]::Split($inner, '(?<!\\)\|') | ForEach-Object { $_.Replace('\|', '|').Trim() })
}

function Get-CellText([string] $Cell) {
    return $Cell.Replace('`', '')
}

# Every markdown table of the page, as a header and rows keyed by header.
function Read-MarkdownTables([string[]] $Lines) {
    $tables = New-Object System.Collections.Generic.List[object]
    $index = 0
    while ($index -lt $Lines.Count) {
        $isHeader = $Lines[$index].StartsWith('|') -and ($index + 1) -lt $Lines.Count -and $Lines[$index + 1] -match '^\|[\s:|-]+\|\s*$'
        if (-not $isHeader) { $index++; continue }
        $header = @(Split-TableRow $Lines[$index] | ForEach-Object { Get-CellText $_ })
        $rows = New-Object System.Collections.Generic.List[object]
        $index += 2
        while ($index -lt $Lines.Count -and $Lines[$index].StartsWith('|')) {
            $cells = @(Split-TableRow $Lines[$index])
            if ($cells.Count -ne $header.Count) { throw "Row $($index + 1) of the table has $($cells.Count) cells; the header '$($header -join ' | ')' has $($header.Count). A '|' inside a cell must be written \|." }
            $row = [ordered]@{}
            for ($column = 0; $column -lt $header.Count; $column++) { $row[$header[$column]] = $cells[$column] }
            $rows.Add($row)
            $index++
        }
        $tables.Add([pscustomobject]@{ Header = $header; Rows = $rows })
    }
    return $tables
}

function ConvertTo-SwitchName([string] $Cell) {
    $text = Get-CellText $Cell
    if ($text -eq '-' -or $text -eq '') { return '' }
    $match = [regex]::Match($text, '^-(\w+)$')
    if (-not $match.Success) { throw "'$text' is not a switch name (a dash and a word) or '-'." }
    return $match.Groups[1].Value
}

# The command of a row, read for what the script needs: the package, the -run pattern, the tags and the
# environment variables it sets.
function ConvertTo-CommandPlan([string] $Command) {
    $text = Get-CellText $Command
    $package = [regex]::Match($text, '\s(\./\S+)$')
    $run = [regex]::Match($text, "-run '([^']+)'")
    $tags = [regex]::Match($text, '-tags (\S+)')
    if (-not $package.Success) { throw "No package (./...) at the end of: $text" }
    if (-not $run.Success) { throw "No -run '<pattern>' in: $text" }
    if (-not $tags.Success) { throw "No -tags in: $text" }
    $environment = [ordered]@{}
    foreach ($assignment in [regex]::Matches($text, '\$env:(\w+)=''([^'']*)''')) {
        $environment[$assignment.Groups[1].Value] = $assignment.Groups[2].Value
    }
    return [pscustomobject]@{
        Text        = $text
        Package     = $package.Groups[1].Value
        Pattern     = $run.Groups[1].Value
        Tags        = $tags.Groups[1].Value
        Environment = $environment
    }
}

function Read-TestPlan([string] $Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "The table of tests is not there: $Path" }
    $tables = Read-MarkdownTables ([IO.File]::ReadAllLines($Path, [Text.Encoding]::UTF8))
    $groupTable = @($tables | Where-Object { $_.Header[0] -eq 'Group' -and $_.Header -contains 'Default' })
    $testTable = @($tables | Where-Object { $_.Header[0] -eq 'Test' -and $_.Header -contains 'Command' })
    $harmlessTable = @($tables | Where-Object { $_.Header[0] -eq 'Test' -and $_.Header -contains 'Package' -and $_.Header -notcontains 'Command' })
    if ($groupTable.Count -ne 1) { throw "$Path must have exactly one Groups table (first column Group, with a Default column); it has $($groupTable.Count)." }
    if ($testTable.Count -ne 1) { throw "$Path must have exactly one Tests table (first column Test, with a Command column); it has $($testTable.Count)." }
    if ($harmlessTable.Count -ne 1) { throw "$Path must have exactly one table of tests that change nothing (first column Test, with a Package column); it has $($harmlessTable.Count)." }

    $groups = @($groupTable[0].Rows | ForEach-Object {
            [pscustomobject]@{
                Name    = Get-CellText $_['Group']
                Default = (Get-CellText $_['Default']) -eq 'yes'
                Switch  = ConvertTo-SwitchName $_['Switch']
                Risk    = Get-CellText $_['Risk']
                Summary = Get-CellText $_['What it checks']
            }
        })
    $tests = @($testTable[0].Rows | ForEach-Object {
            $command = ConvertTo-CommandPlan $_['Command']
            [pscustomobject]@{
                Test     = Get-CellText $_['Test']
                Group    = Get-CellText $_['Group']
                Command  = $command
                Changes  = Get-CellText $_['Changes']
                Expected = Get-CellText $_['Expected']
                Cleanup  = Get-CellText $_['Cleanup']
                Risk     = Get-CellText $_['Risk']
                Switch   = ConvertTo-SwitchName $_['Switch']
            }
        })
    $harmless = @($harmlessTable[0].Rows | ForEach-Object { [pscustomobject]@{ Test = Get-CellText $_['Test']; Package = Get-CellText $_['Package'] } })
    return [pscustomobject]@{ Groups = $groups; Tests = $tests; Harmless = $harmless }
}

function Get-SwitchValue([string] $Name) {
    switch ($Name) {
        'TestMachine' { return [bool] $TestMachine }
        'CatchAll' { return [bool] $CatchAll }
        'IAcceptOpenVpnInterruption' { return [bool] $IAcceptOpenVpnInterruption }
        default { throw "The table names the switch -$Name, which this script does not have." }
    }
}

# Problems with the plan that no host is needed to find.
function Test-PlanConsistency($Plan) {
    $problems = New-Object System.Collections.Generic.List[string]
    $groupNames = @($Plan.Groups | ForEach-Object { $_.Name })
    foreach ($duplicate in ($groupNames | Group-Object | Where-Object Count -gt 1)) { $problems.Add("Group $($duplicate.Name) is listed twice.") }
    foreach ($group in $Plan.Groups) {
        if (-not @($Plan.Tests | Where-Object Group -eq $group.Name)) { $problems.Add("Group $($group.Name) has no test.") }
        if ($group.Switch) { try { [void] (Get-SwitchValue $group.Switch) } catch { $problems.Add($_.Exception.Message) } }
    }
    $seen = @{}
    foreach ($test in $Plan.Tests) {
        $key = $test.Command.Package + ' ' + $test.Test
        if ($seen.ContainsKey($key)) { $problems.Add("$key is listed twice.") }
        $seen[$key] = $true
        if ($groupNames -notcontains $test.Group) { $problems.Add("$($test.Test) is in the group $($test.Group), which the Groups table does not have.") }
        if ($test.Command.Pattern -ne ('^' + $test.Test + '$')) { $problems.Add("$($test.Test): the -run pattern '$($test.Command.Pattern)' is not '^$($test.Test)`$'.") }
        if ($test.Command.Tags -ne $TestBinaryTags) { $problems.Add("$($test.Test): the tags are '$($test.Command.Tags)', not '$TestBinaryTags'.") }
        if (-not (Test-Path -LiteralPath (Join-Path $RepositoryRoot $test.Command.Package) -PathType Container)) { $problems.Add("$($test.Test): the package $($test.Command.Package) is not a folder of the repository.") }
        if ($test.Switch) { try { [void] (Get-SwitchValue $test.Switch) } catch { $problems.Add($_.Exception.Message) } }
    }
    return $problems.ToArray()
}

# --- Verdicts ------------------------------------------------------------------------------------------------------

# What a test binary run with -test.v says about the one test it was asked to run. A pass needs the test's own
# "--- PASS" line: a pattern that matches nothing exits 0 too, and a skipped test is not a passed one.
function Get-RowVerdict {
    param([string] $TestName, [int] $ExitCode, [string[]] $Output)
    $marks = @{}
    foreach ($line in $Output) {
        $match = [regex]::Match($line, '^--- (PASS|FAIL|SKIP): (\S+)')
        if ($match.Success -and $match.Groups[2].Value -eq $TestName) { $marks[$match.Groups[1].Value] = $true }
    }
    if ($marks.ContainsKey('FAIL') -or $ExitCode -ne 0) { return $VerdictFailed }
    if ($marks.ContainsKey('PASS')) { return $VerdictPassed }
    if ($marks.ContainsKey('SKIP')) { return $VerdictSkipped }
    return $VerdictNotRun
}

# Why a test skipped: the log line before its "--- SKIP" line.
function Get-SkipReason([string] $TestName, [string[]] $Output) {
    for ($index = 1; $index -lt $Output.Count; $index++) {
        if ($Output[$index] -match ('^--- SKIP: ' + [regex]::Escape($TestName) + ' ')) { return $Output[$index - 1].Trim() }
    }
    return ''
}

# --- The machine ---------------------------------------------------------------------------------------------------

function Test-Elevated {
    $principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Test-ScratchPrefix([string] $Prefix) {
    $text = $Prefix.ToLowerInvariant()
    if ($text.StartsWith($ScratchIPv6Start)) { return $true }
    $match = [regex]::Match($text, '^(\d+)\.(\d+)\.(\d+)\.(\d+)/(\d+)$')
    if (-not $match.Success) { return $false }
    $address = [uint64] 0
    foreach ($part in 1..4) { $address = $address * 256 + [uint64] $match.Groups[$part].Value }
    $length = [int] $match.Groups[5].Value
    foreach ($range in $ScratchIPv4Ranges) {
        $rangeParts = $range.Split('/')
        $rangeAddress = [uint64] 0
        foreach ($octet in $rangeParts[0].Split('.')) { $rangeAddress = $rangeAddress * 256 + [uint64] $octet }
        $rangeLength = [int] $rangeParts[1]
        $hostBits = 32 - $rangeLength
        $sameNetwork = [math]::Floor($address / [math]::Pow(2, $hostBits)) -eq [math]::Floor($rangeAddress / [math]::Pow(2, $hostBits))
        if ($length -ge $rangeLength -and $sameNetwork) { return $true }
    }
    return $false
}

function Get-RouteLines {
    return @(Get-NetRoute -PolicyStore ActiveStore -ErrorAction SilentlyContinue |
            ForEach-Object { '{0} via {1} on {2}' -f $_.DestinationPrefix, $_.NextHop, $_.InterfaceAlias })
}

function Get-DnsRuleNames([string] $Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    return @(Get-ChildItem -LiteralPath $Path -ErrorAction SilentlyContinue | ForEach-Object { $_.PSChildName })
}

function Get-AdapterLines {
    return @(Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | ForEach-Object { '{0} | {1}' -f $_.Name, $_.InterfaceDescription })
}

# The rule's name and its text, which names the program a rule is for.
function Get-FirewallLines {
    if (-not (Test-Path -LiteralPath $FirewallRulesPath)) { return @() }
    $key = Get-Item -LiteralPath $FirewallRulesPath
    return @($key.GetValueNames() | ForEach-Object { '{0} :: {1}' -f $_, $key.GetValue($_) })
}

function Get-DnsClientLines {
    $lines = New-Object System.Collections.Generic.List[string]
    $global = Get-DnsClientGlobalSetting -ErrorAction SilentlyContinue
    if ($global) { $lines.Add(('global suffixes={0} useSuffixSearchList={1} useDevolution={2}' -f ($global.SuffixSearchList -join ','), $global.UseSuffixSearchList, $global.UseDevolution)) }
    foreach ($client in @(Get-DnsClient -ErrorAction SilentlyContinue)) {
        $lines.Add(('{0} suffix={1} register={2}' -f $client.InterfaceAlias, $client.ConnectionSpecificSuffix, $client.RegisterThisConnectionsAddress))
    }
    foreach ($server in @(Get-DnsClientServerAddress -ErrorAction SilentlyContinue)) {
        $lines.Add(('{0} {1} servers={2}' -f $server.InterfaceAlias, $server.AddressFamily, ($server.ServerAddresses -join ',')))
    }
    return $lines.ToArray()
}

function Get-LoopbackLines {
    $lines = New-Object System.Collections.Generic.List[string]
    foreach ($address in @(Get-NetIPAddress -InterfaceAlias $LoopbackAlias -ErrorAction SilentlyContinue)) {
        $lines.Add(('address {0}/{1}' -f $address.IPAddress, $address.PrefixLength))
    }
    foreach ($interface in @(Get-NetIPInterface -InterfaceAlias $LoopbackAlias -ErrorAction SilentlyContinue)) {
        $lines.Add(('metric {0} {1} automatic={2}' -f $interface.AddressFamily, $interface.InterfaceMetric, $interface.AutomaticMetric))
    }
    return $lines.ToArray()
}

# The state of the real service, and nothing else about it: the script does not call anything on it.
function Get-RealServiceLine {
    $service = Get-Service -Name $RealServiceName -ErrorAction SilentlyContinue
    if (-not $service) { return @('absent') }
    return @('{0} {1}' -f $service.Status, $service.StartType)
}

# The OpenVPN of the person whose PC this is: its processes and its adapters, so that a run next to it can show
# that they are still there.
function Get-OwnerOpenVpnLines {
    $lines = New-Object System.Collections.Generic.List[string]
    foreach ($process in @(Get-Process -Name openvpn -ErrorAction SilentlyContinue)) { $lines.Add(('process openvpn pid {0}' -f $process.Id)) }
    foreach ($adapter in @(Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue)) {
        if ($adapter.InterfaceDescription -match $OwnerOpenVpnPattern -and -not $adapter.Name.StartsWith($ProductMarker)) {
            $lines.Add(('adapter {0} | {1}' -f $adapter.Name, $adapter.InterfaceDescription))
        }
    }
    return $lines.ToArray()
}

function Get-DriverPackageNames {
    if (-not (Test-Path -LiteralPath $DriverStorePath)) { return @() }
    return @(Get-ChildItem -LiteralPath $DriverStorePath -Directory -ErrorAction SilentlyContinue | ForEach-Object { $_.Name })
}

function Get-ScratchDirectoryNames {
    return @(Get-ChildItem -LiteralPath $env:ProgramFiles -Directory -Filter $ScratchDirectoryFilter -ErrorAction SilentlyContinue | ForEach-Object { $_.Name })
}

# What the tests may change, in a form that two snapshots can be compared in. Every value is a list of lines.
function Get-HostSnapshot {
    return [ordered]@{
        Routes             = @(Get-RouteLines)
        DnsRules           = @(Get-DnsRuleNames $DnsRulesPath)
        PolicyDnsRules     = @(Get-DnsRuleNames $PolicyDnsRulesPath)
        Adapters           = @(Get-AdapterLines)
        Services           = @(Get-Service -ErrorAction SilentlyContinue | ForEach-Object { $_.Name })
        RealService        = @(Get-RealServiceLine)
        Firewall           = @(Get-FirewallLines)
        DnsClient          = @(Get-DnsClientLines)
        Loopback           = @(Get-LoopbackLines)
        OwnerOpenVpn       = @(Get-OwnerOpenVpnLines)
        Drivers            = @(Get-DriverPackageNames)
        ScratchDirectories = @(Get-ScratchDirectoryNames)
    }
}

# How serious a difference is. The first rule that matches decides; a difference no rule names is a change.
$SeverityRules = @(
    @{ Category = 'Adapters'; Change = 'added'; Pattern = '^' + [regex]::Escape($ProductMarker); Severity = $SeverityLeftover },
    @{ Category = 'DnsRules'; Change = 'added'; Pattern = '^' + [regex]::Escape($ProductMarker); Severity = $SeverityLeftover },
    @{ Category = 'Routes'; Change = 'added'; Scratch = $true; Severity = $SeverityLeftover },
    @{ Category = 'Loopback'; Change = '*'; Pattern = '.'; Severity = $SeverityLeftover },
    @{ Category = 'Services'; Change = 'added'; Pattern = '^Plaitway'; Severity = $SeverityLeftover },
    @{ Category = 'RealService'; Change = '*'; Pattern = '.'; Severity = $SeverityLeftover },
    @{ Category = 'ScratchDirectories'; Change = 'added'; Pattern = '.'; Severity = $SeverityLeftover },
    @{ Category = 'Firewall'; Change = 'added'; Pattern = '(?i)plaitway|' + [regex]::Escape($TestBinarySuffix); Severity = $SeverityLeftover },
    @{ Category = 'OwnerOpenVpn'; Change = 'removed'; Pattern = '.'; Severity = $SeverityLeftover },
    @{ Category = 'Drivers'; Change = 'added'; Pattern = '.'; Severity = $SeverityNote }
)

function Get-FindingSeverity([string] $Category, [string] $Change, [string] $Item) {
    foreach ($rule in $SeverityRules) {
        if ($rule.Category -ne $Category) { continue }
        if ($rule.Change -ne '*' -and $rule.Change -ne $Change) { continue }
        if ($rule.ContainsKey('Scratch')) {
            if (Test-ScratchPrefix ($Item -split ' via ')[0]) { return $rule.Severity }
            continue
        }
        if ($Item -match $rule.Pattern) { return $rule.Severity }
    }
    return $SeverityChanged
}

function Compare-HostSnapshot($Before, $After) {
    $findings = New-Object System.Collections.Generic.List[object]
    foreach ($category in $Before.Keys) {
        $beforeSet = New-Object 'System.Collections.Generic.HashSet[string]' (, [string[]] @($Before[$category]))
        $afterSet = New-Object 'System.Collections.Generic.HashSet[string]' (, [string[]] @($After[$category]))
        foreach ($item in @($After[$category])) {
            if (-not $beforeSet.Contains($item)) { $findings.Add([pscustomobject]@{ Category = $category; Change = 'added'; Item = $item; Severity = (Get-FindingSeverity $category 'added' $item) }) }
        }
        foreach ($item in @($Before[$category])) {
            if (-not $afterSet.Contains($item)) { $findings.Add([pscustomobject]@{ Category = $category; Change = 'removed'; Item = $item; Severity = (Get-FindingSeverity $category 'removed' $item) }) }
        }
    }
    return $findings.ToArray()
}

# The commands that undo what the findings say is left, to print next to them.
function Get-RecoveryCommands($Findings) {
    $commands = New-Object System.Collections.Generic.List[string]
    $leftovers = @($Findings | Where-Object Severity -eq $SeverityLeftover)
    foreach ($finding in $leftovers) {
        switch ($finding.Category) {
            'Adapters' {
                $commands.Add("Get-NetAdapter -Name 'Plaitway-*' -IncludeHidden | ForEach-Object { pnputil.exe /remove-device `$_.PnPDeviceID }")
                $commands.Add("Get-NetAdapter -Name 'Plaitway-test-ovpn-*' | ForEach-Object { & '$OpenVpnTapctl' delete `$_.Name }")
            }
            'DnsRules' {
                $commands.Add("Get-ChildItem '$DnsRulesPath' | Where-Object PSChildName -like 'Plaitway-*' | Remove-Item; Clear-DnsClientCache")
            }
            'Routes' {
                $commands.Add(('Remove-NetRoute -DestinationPrefix {0} -Confirm:$false' -f ($finding.Item -split ' via ')[0]))
            }
            'Loopback' {
                if ($finding.Item -match '^address (.+)/\d+$' -and $finding.Change -eq 'added') {
                    $commands.Add(("Remove-NetIPAddress -InterfaceAlias '$LoopbackAlias' -IPAddress {0} -Confirm:`$false" -f $Matches[1]))
                }
                else {
                    $commands.Add("Set-NetIPInterface -InterfaceAlias '$LoopbackAlias' -AddressFamily IPv4 -AutomaticMetric Enabled; Set-NetIPInterface -InterfaceAlias '$LoopbackAlias' -AddressFamily IPv6 -AutomaticMetric Enabled   # or the metric of the before snapshot: $($finding.Item)")
                }
            }
            'Services' {
                $commands.Add("sc.exe stop $TestServiceName; sc.exe delete $TestServiceName")
            }
            'ScratchDirectories' {
                $commands.Add("Remove-Item -Recurse -Force (Join-Path `$env:ProgramFiles '$($finding.Item)')")
            }
            'Firewall' {
                $commands.Add(('Remove-NetFirewallRule -Name ''{0}''' -f ($finding.Item -split ' :: ')[0]))
            }
            'RealService' {
                $commands.Add('packaging\windows\service\Show-PlaitwayService.ps1   # the script never changes this service; look at what did')
            }
            'OwnerOpenVpn' {
                $commands.Add('Reconnect the OpenVPN GUI or start its service again; the tests ended or removed something of it')
            }
        }
    }
    return @($commands | Select-Object -Unique)
}

function Show-Findings($Findings) {
    $order = @($SeverityLeftover, $SeverityChanged, $SeverityNote)
    $titles = @{ $SeverityLeftover = 'LEFT BEHIND'; $SeverityChanged = 'changed (not by the tests, or not known to be)'; $SeverityNote = 'documented side effect' }
    $colors = @{ $SeverityLeftover = 'Red'; $SeverityChanged = 'Yellow'; $SeverityNote = 'Gray' }
    foreach ($severity in $order) {
        $group = @($Findings | Where-Object Severity -eq $severity)
        if ($group.Count -eq 0) { continue }
        Write-Line ('  {0}: {1}' -f $titles[$severity], $group.Count) $colors[$severity]
        foreach ($finding in $group) {
            $item = $finding.Item
            if ($item.Length -gt 200) { $item = $item.Substring(0, 200) + '...' }
            Write-Line ('    {0,-7} {1,-18} {2}' -f $finding.Change, $finding.Category, $item) $colors[$severity]
        }
    }
    $recovery = @(Get-RecoveryCommands $Findings)
    if ($recovery.Count -gt 0) {
        Write-Line '  To undo it by hand, in an elevated PowerShell:' 'Red'
        $recovery | ForEach-Object { Write-Line ('    ' + $_) 'Red' }
    }
}

# --- Preconditions -------------------------------------------------------------------------------------------------

# Empty when 127.0.0.1:53 on UDP can be bound, which is what the throw-away resolver of the tests does; otherwise
# why not. Trying is the only reliable answer: whether another socket on the port gets in the way depends on how
# that socket was bound.
function Get-DnsPortProblem {
    $socket = New-Object System.Net.Sockets.Socket([Net.Sockets.AddressFamily]::InterNetwork, [Net.Sockets.SocketType]::Dgram, [Net.Sockets.ProtocolType]::Udp)
    try {
        $socket.Bind((New-Object System.Net.IPEndPoint([Net.IPAddress]::Loopback, $DnsPort)))
        return ''
    }
    catch [System.Net.Sockets.SocketException] {
        return "UDP 127.0.0.1:$DnsPort cannot be bound ($($_.Exception.Message)); the throw-away resolver of the tests listens there."
    }
    finally {
        $socket.Close()
    }
}

function Get-OwnerOpenVpnProcesses {
    return @(Get-Process -Name openvpn -ErrorAction SilentlyContinue)
}

function Get-AdapterNamesStartingWith([string] $Prefix) {
    return @(Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | Where-Object { $_.Name.StartsWith($Prefix, [StringComparison]::OrdinalIgnoreCase) } | ForEach-Object { $_.Name })
}

function Get-WintunDllPath {
    if ($WintunDll) { return $WintunDll }
    return $DefaultWintunDll
}

# The real service must be stopped or absent while a test touches routes or rules: what it does in the minutes a test
# takes would meet the test's changes, and its start-up sweep would remove the test's rules.
function Get-HelperServiceProblem {
    $service = Get-Service -Name $RealServiceName -ErrorAction SilentlyContinue
    if ($service -and $service.Status -ne 'Stopped') { return "The $RealServiceName service is $($service.Status); stop it first (plaitwayd.exe stop). The script never stops it." }
    return ''
}

# One check per group, by the name the table gives it. Each returns the reasons to refuse.
$PreconditionChecks = @{
    Routes  = {
        $problems = @()
        $helper = Get-HelperServiceProblem
        if ($helper) { $problems += $helper }
        $present = @(Get-RouteLines | Where-Object { Test-ScratchPrefix (($_ -split ' via ')[0]) })
        if ($present.Count -gt 0) { $problems += "A scratch destination already has a route: $($present -join '; ')" }
        return $problems
    }
    Dns     = {
        $problems = @()
        $helper = Get-HelperServiceProblem
        if ($helper) { $problems += $helper }
        $portProblem = Get-DnsPortProblem
        if ($portProblem) { $problems += $portProblem }
        $rules = @(Get-DnsRuleNames $DnsRulesPath | Where-Object { $_.StartsWith($ProductMarker) })
        if (@($rules).Count -gt 0) { $problems += "NRPT rules named $ProductMarker* exist ($(@($rules) -join ', ')); the tests remove every such rule at the end." }
        if (@(Get-DnsRuleNames $PolicyDnsRulesPath).Count -gt 0) { $problems += 'A group policy delivers NRPT rules, and Windows then ignores the rules of local programs; the tests would fail with ErrGroupPolicyNRPT.' }
        return $problems
    }
    Wintun  = {
        $problems = @()
        $dll = Get-WintunDllPath
        if (-not (Test-Path -LiteralPath $dll -PathType Leaf)) { $problems += "No wintun.dll at $dll. Fetch the checked copy: powershell -File packaging\windows\fetch-wintun.ps1 -OutputDirectory bin\wintun" }
        $left = @(Get-AdapterNamesStartingWith $ScratchAdapterPrefix)
        if ($left.Count -gt 0) { $problems += "Adapters named $ScratchAdapterPrefix* exist ($($left -join ', ')); remove them first." }
        return $problems
    }
    OpenVpn = {
        $problems = @()
        if (-not (Test-Path -LiteralPath $OpenVpnProgram -PathType Leaf)) { $problems += "No OpenVPN at $OpenVpnProgram; the tests would skip." }
        $running = @(Get-OwnerOpenVpnProcesses)
        if ($running.Count -gt 0 -and -not $IAcceptOpenVpnInterruption) {
            $problems += "openvpn.exe runs (pid $((@($running) | ForEach-Object { $_.Id }) -join ', ')). Disconnect it, or give -IAcceptOpenVpnInterruption to run next to it."
        }
        $left = @(Get-AdapterNamesStartingWith $ScratchAdapterPrefix)
        if ($left.Count -gt 0) { $problems += "Adapters named $ScratchAdapterPrefix* exist ($($left -join ', ')); remove them first." }
        return $problems
    }
    Daemon  = {
        $problems = @()
        $helper = Get-HelperServiceProblem
        if ($helper) { $problems += $helper }
        $rules = @(Get-DnsRuleNames $DnsRulesPath | Where-Object { $_.StartsWith($ProductMarker) })
        if ($rules.Count -gt 0) { $problems += "NRPT rules named $ProductMarker* exist ($($rules -join ', ')); the daemon removes them at start." }
        return $problems
    }
    Service = {
        $problems = @()
        if (Get-Service -Name $TestServiceName -ErrorAction SilentlyContinue) { $problems += "$TestServiceName exists already; remove it first: sc.exe delete $TestServiceName" }
        return $problems
    }
}

# What the script itself needs, whatever the groups.
function Get-ToolProblems {
    $problems = @()
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { $problems += 'go is not on the path; the test binaries are built from this tree, and the Service group builds the daemon.' }
    if (-not (Test-Path -LiteralPath (Join-Path $RepositoryRoot 'go.mod'))) { $problems += "$RepositoryRoot is not the repository (no go.mod)." }
    return $problems
}

function Get-UnmergedFiles {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { return @() }
    $status = @(& git -C $RepositoryRoot status --porcelain 2>$null)
    return @($status | Where-Object { $_ -match '^(DD|AU|UD|UA|DU|AA|UU) ' })
}

function Get-TreeSummary {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { return 'git is not installed' }
    $head = (& git -C $RepositoryRoot rev-parse --short HEAD 2>$null)
    $changed = @(& git -C $RepositoryRoot status --porcelain 2>$null).Count
    return "HEAD $head, $changed changed files (the tests are built from the working tree, not from HEAD)"
}

# --- Plan ----------------------------------------------------------------------------------------------------------

function Resolve-SelectedGroups($Plan) {
    # powershell.exe -File passes -Include A,B as the one string "A,B".
    $requested = @($Include | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim() } | Where-Object { $_ })
    if ($requested.Count -eq 0) { return @($Plan.Groups | Where-Object Default) }
    $unknown = @($requested | Where-Object { $name = $_; -not @($Plan.Groups | Where-Object { $_.Name -eq $name }) })
    if ($unknown.Count -gt 0) { throw "Unknown group $($unknown -join ', '). The table has: $(($Plan.Groups | ForEach-Object { $_.Name }) -join ', ')." }
    # The order of the table is the dependency order, whatever order was typed.
    return @($Plan.Groups | Where-Object { $name = $_.Name; @($requested | Where-Object { $_ -eq $name }).Count -gt 0 })
}

function Get-GroupTests($Plan, $Group) {
    return @($Plan.Tests | Where-Object Group -eq $Group.Name)
}

function Test-TestRunnable($Test) {
    return (-not $Test.Switch) -or (Get-SwitchValue $Test.Switch)
}

function Get-GroupRefusals($Plan, $Group) {
    $reasons = @()
    if ($Group.Switch -and -not (Get-SwitchValue $Group.Switch)) {
        $reasons += "The group $($Group.Name) needs -$($Group.Switch): $($Group.Summary)."
    }
    if ($PreconditionChecks.ContainsKey($Group.Name)) { $reasons += @(& $PreconditionChecks[$Group.Name]) }
    return $reasons
}

function Show-GroupPlan($Plan, $Group) {
    Write-Heading ('Group {0}  (risk to a daily machine: {1})' -f $Group.Name, $Group.Risk)
    Write-Line $Group.Summary
    foreach ($test in (Get-GroupTests $Plan $Group)) {
        if (-not (Test-TestRunnable $Test)) {
            Write-Line ('  left out   {0}  (needs -{1})' -f $Test.Test, $Test.Switch) 'DarkGray'
            continue
        }
        Write-Line ('  {0}   [{1}]' -f $Test.Test, $Test.Command.Package)
        Write-Line ('      changes: ' + $Test.Changes)
        Write-Line ('      cleanup: ' + $Test.Cleanup)
    }
}

function Show-Switches {
    Write-Line ('Switches: -Yes {0}, -TestMachine {1}, -CatchAll {2}, -IAcceptOpenVpnInterruption {3}' -f [bool] $Yes, [bool] $TestMachine, [bool] $CatchAll, [bool] $IAcceptOpenVpnInterruption)
}

# --- The dry run ---------------------------------------------------------------------------------------------------

function Show-DryRun($Plan, $Selected) {
    Write-Heading 'Plan (nothing is changed by -WhatIf)'
    Write-Line ('Tests table      {0}  ({1} groups, {2} tests)' -f $TablePath, $Plan.Groups.Count, $Plan.Tests.Count)
    Write-Line ('Tree             ' + (Get-TreeSummary))
    Write-Line ('Binaries and log {0}' -f $OutputDirectory)
    Show-Switches
    $elevated = Test-Elevated
    Write-Line ('Elevated         {0}{1}' -f $elevated, $(if ($elevated) { '' } else { '  (the real run refuses to start in this shell)' }))
    foreach ($problem in (Get-ToolProblems)) { Write-Line ('Would refuse     ' + $problem) 'Yellow' }
    $unmerged = @(Get-UnmergedFiles)
    if ($unmerged.Count -gt 0) { Write-Line ('Would refuse     unmerged files: ' + ($unmerged -join '; ')) 'Yellow' }

    $refused = 0
    foreach ($group in $Selected) {
        Show-GroupPlan $Plan $group
        $reasons = @(Get-GroupRefusals $Plan $group)
        if ($reasons.Count -eq 0) { Write-Line '  preconditions: all met' 'Green' }
        foreach ($reason in $reasons) { Write-Line ('  would refuse: ' + $reason) 'Yellow'; $refused++ }
    }
    $notSelected = @($Plan.Groups | Where-Object { $Selected -notcontains $_ } | ForEach-Object { $_.Name })
    if ($notSelected.Count -gt 0) { Write-Line ''; Write-Line ('Not selected: {0}. Name them with -Include.' -f ($notSelected -join ', ')) 'DarkGray' }
    Write-Line ''
    if ($refused -gt 0) { Write-Line 'This run would be refused as it stands (see above).' 'Yellow' }
    elseif (-not $elevated) { Write-Line 'The preconditions are met; the real run needs an elevated shell.' 'Yellow' }
    else { Write-Line 'This run would start.' 'Green' }
}

# --- Building and running ------------------------------------------------------------------------------------------

function Invoke-NativeLines {
    param([string] $FilePath, [string[]] $Arguments, [string] $WorkingDirectory, [bool] $Echo)
    $lines = New-Object System.Collections.Generic.List[string]
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    Push-Location $WorkingDirectory
    try {
        & $FilePath @Arguments 2>&1 | ForEach-Object {
            $line = "$_"
            $lines.Add($line)
            if ($Echo) { Write-Line ('    ' + $line) }
        }
        $exitCode = $LASTEXITCODE
    }
    finally {
        Pop-Location
        $ErrorActionPreference = $previousPreference
    }
    return [pscustomobject]@{ ExitCode = $exitCode; Output = $lines.ToArray() }
}

function Get-TestBinaryPath([string] $Package) {
    $slug = ($Package.TrimStart('.', '/') -replace '[^A-Za-z0-9]+', '_')
    return Join-Path $OutputDirectory ($slug + $TestBinarySuffix)
}

function New-TestBinaries($Tests) {
    $built = @{}
    foreach ($package in ($Tests | ForEach-Object { $_.Command.Package } | Select-Object -Unique)) {
        $exe = Get-TestBinaryPath $package
        $tags = ($Tests | Where-Object { $_.Command.Package -eq $package } | Select-Object -First 1).Command.Tags
        Write-Line ('Building {0} -> {1}' -f $package, $exe)
        $result = Invoke-NativeLines 'go' @('test', '-c', '-tags', $tags, '-o', $exe, $package) $RepositoryRoot $false
        if ($result.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $exe)) {
            $result.Output | Select-Object -Last 30 | ForEach-Object { Write-Line ('    ' + $_) 'Red' }
            throw "go test -c failed for $package."
        }
        $built[$package] = $exe
    }
    return $built
}

function Invoke-TestRow($Test, [string] $Binary) {
    $arguments = @('-test.run', $Test.Command.Pattern, '-test.count=1', '-test.v', "-test.timeout=${TestTimeoutMinutes}m")
    $environment = [ordered]@{}
    foreach ($name in $Test.Command.Environment.Keys) { $environment[$name] = $Test.Command.Environment[$name] }
    if ($WintunDll) { $environment['PLAITWAY_WINTUN_DLL'] = (Resolve-Path -LiteralPath $WintunDll).ProviderPath }
    if ($IAcceptOpenVpnInterruption) { $environment['PLAITWAY_ROOT_ALLOW_OWNER_OPENVPN'] = '1' }
    $previous = @{}
    foreach ($name in $environment.Keys) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $environment[$name], 'Process')
    }
    try {
        $directory = Join-Path $RepositoryRoot $Test.Command.Package
        $result = Invoke-NativeLines $Binary $arguments $directory $true
    }
    finally {
        foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    }
    $verdict = Get-RowVerdict $Test.Test $result.ExitCode $result.Output
    return [pscustomobject]@{
        Test     = $Test.Test
        Package  = $Test.Command.Package
        Verdict  = $verdict
        ExitCode = $result.ExitCode
        Findings = @($result.Output | Where-Object { $_ -match $FindingPattern } | ForEach-Object { $_.Trim() })
        Reason   = $(if ($verdict -eq $VerdictSkipped) { Get-SkipReason $Test.Test $result.Output } else { '' })
    }
}

function Start-SocketWatcher([string] $WatcherPath, [string] $LogFile) {
    if (Test-Path -LiteralPath $LogFile) { Remove-Item -LiteralPath $LogFile -Force }
    $process = Start-Process -FilePath $WatcherPath -PassThru -WindowStyle Hidden -RedirectStandardOutput $LogFile
    Start-Sleep -Milliseconds 400
    return $process
}

function Stop-SocketWatcher($Process, [string] $LogFile) {
    Start-Sleep -Milliseconds 400
    Stop-Process -Id $Process.Id -Force
    if (-not (Test-Path -LiteralPath $LogFile)) { return @() }
    return @(Get-Content -LiteralPath $LogFile | Where-Object { $_ })
}

# The guard processes of the tests write a log each; the owner wants them with the run, and one that had to act is
# a finding.
function Copy-GuardLogs([datetime] $Since, [string] $Destination) {
    $acted = @()
    foreach ($file in @(Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Filter $GuardLogFilter -ErrorAction SilentlyContinue | Where-Object { $_.LastWriteTime -ge $Since })) {
        Copy-Item -LiteralPath $file.FullName -Destination $Destination -Force
        if (@(Select-String -LiteralPath $file.FullName -Pattern $GuardActedPattern -ErrorAction SilentlyContinue).Count -gt 0) { $acted += $file.Name }
    }
    return $acted
}

function Confirm-Group($Group) {
    if ($Yes) { return $true }
    try {
        $answer = Read-Host ("Type {0} to run this group, anything else to skip it" -f $Group.Name)
    }
    catch {
        Write-Line 'No console to ask on; give -Yes to run without the question.' 'Yellow'
        return $false
    }
    return $answer.Trim() -eq $Group.Name
}

function Save-Snapshot($Snapshot, [string] $Name) {
    $path = Join-Path $script:LogDirectory ('snapshot-' + $Name + '.json')
    $Snapshot | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $path -Encoding UTF8
}

function Invoke-Group($Plan, $Group, $Binaries, $WatcherPath) {
    $result = [pscustomobject]@{ Group = $Group.Name; Declined = $false; Rows = @(); Findings = @(); Sockets = @(); GuardsActed = @(); Leftovers = $false }
    $tests = @(Get-GroupTests $Plan $Group | Where-Object { Test-TestRunnable $_ })
    $started = Get-Date

    $before = Get-HostSnapshot
    Save-Snapshot $before ($Group.Name + '-before')
    $watcherLog = Join-Path $script:LogDirectory ('sockets-' + $Group.Name + '.log')
    $watcher = Start-SocketWatcher $WatcherPath $watcherLog
    $rows = @()
    try {
        foreach ($test in $tests) {
            Write-Heading ('{0}: {1}' -f $Group.Name, $test.Test)
            $rows += Invoke-TestRow $test $Binaries[$test.Command.Package]
            $row = $rows[-1]
            $color = $(if ($row.Verdict -eq $VerdictPassed) { 'Green' } else { 'Red' })
            Write-Line ('  -> {0}' -f $row.Verdict) $color
        }
    }
    finally {
        $result.Sockets = @(Stop-SocketWatcher $watcher $watcherLog)
    }
    $result.Rows = $rows

    $after = Get-HostSnapshot
    Save-Snapshot $after ($Group.Name + '-after')
    $result.Findings = @(Compare-HostSnapshot $before $after)
    $result.GuardsActed = @(Copy-GuardLogs $started $script:LogDirectory)

    Write-Heading ('After {0}' -f $Group.Name)
    if ($result.Findings.Count -eq 0) { Write-Line '  no difference in routes, rules, adapters, services, firewall, DNS settings, loopback and drivers' 'Green' } else { Show-Findings $result.Findings }
    if ($result.Sockets.Count -gt 0) {
        Write-Line '  A test opened a socket on an address that is not loopback; Windows Defender Firewall may have asked about it and may hold a rule:' 'Red'
        $result.Sockets | ForEach-Object { Write-Line ('    ' + $_) 'Red' }
    }
    if ($result.GuardsActed.Count -gt 0) { Write-Line ('  A guard had to clean up after a test: ' + ($result.GuardsActed -join ', ')) 'Red' }
    $result.Leftovers = (@($result.Findings | Where-Object Severity -eq $SeverityLeftover).Count -gt 0) -or $result.Sockets.Count -gt 0 -or $result.GuardsActed.Count -gt 0
    return $result
}

# --- Report ---------------------------------------------------------------------------------------------------------

function Show-Summary($Results) {
    Write-Heading 'Summary'
    foreach ($result in $Results) {
        if ($result.Declined) { Write-Line ('  {0,-8} declined' -f $result.Group) 'Yellow'; continue }
        foreach ($row in $result.Rows) {
            $color = $(if ($row.Verdict -eq $VerdictPassed) { 'Green' } else { 'Red' })
            Write-Line ('  {0,-8} {1,-8} {2}' -f $result.Group, $row.Verdict, $row.Test) $color
            if ($row.Reason) { Write-Line ('           skipped because: ' + $row.Reason) 'Yellow' }
        }
    }
    $findings = @($Results | ForEach-Object { $_.Rows } | ForEach-Object { $_.Findings } | Where-Object { $_ })
    if ($findings.Count -gt 0) {
        Write-Heading 'Findings (the lines that answer the unknowns of Docs\windows-elevated-tests.md)'
        $findings | ForEach-Object { Write-Line ('  ' + $_) }
    }
}

function Get-ExitCode($Results) {
    if (@($Results | Where-Object Leftovers).Count -gt 0) { return $ExitLeftovers }
    foreach ($result in $Results) {
        if ($result.Declined) { return $ExitIncomplete }
        if (@($result.Rows | Where-Object { $_.Verdict -ne $VerdictPassed }).Count -gt 0) { return $ExitIncomplete }
    }
    return $ExitPassed
}

# --- Modes that need no elevation -------------------------------------------------------------------------------------

function Invoke-SnapshotOnly {
    Write-Heading 'Snapshot twice, compare'
    $first = Get-HostSnapshot
    foreach ($name in $first.Keys) { Write-Line ('  {0,-20} {1}' -f $name, @($first[$name]).Count) }
    Start-Sleep -Seconds 2
    $second = Get-HostSnapshot
    $findings = @(Compare-HostSnapshot $first $second)
    if ($findings.Count -eq 0) { Write-Line 'No difference between the two snapshots.' 'Green' } else { Show-Findings $findings }
}

function Get-TestNames([string] $Binary, [string] $Package) {
    $listed = Invoke-NativeLines $Binary @('-test.list', '^Test') (Join-Path $RepositoryRoot $Package) $false
    return @($listed.Output | ForEach-Object { $_.Trim() } | Where-Object { $_ -match '^Test\w+$' })
}

function New-UntaggedTestBinary([string] $Package) {
    $exe = (Get-TestBinaryPath $Package).Replace($TestBinarySuffix, '.untagged' + $TestBinarySuffix)
    $result = Invoke-NativeLines 'go' @('test', '-c', '-o', $exe, $Package) $RepositoryRoot $false
    if ($result.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $exe)) { throw "go test -c failed for $Package without the tag." }
    return $exe
}

function Invoke-CheckTable($Plan) {
    $problems = New-Object System.Collections.Generic.List[string]
    foreach ($problem in (Test-PlanConsistency $Plan)) { $problems.Add($problem) }
    if ($problems.Count -eq 0) {
        $null = New-Item -ItemType Directory -Force -Path $OutputDirectory
        $binaries = New-TestBinaries $Plan.Tests
        foreach ($package in $binaries.Keys) {
            $names = Get-TestNames $binaries[$package] $package
            foreach ($test in ($Plan.Tests | Where-Object { $_.Command.Package -eq $package })) {
                if ($names -notcontains $test.Test) { $problems.Add("$($test.Test) is not a test of $package (with the tag $TestBinaryTags).") }
            }
            # The tests that exist only with the tag are the elevated ones: each is a row of the Tests table, or is
            # named in the table of tests that change nothing.
            $untagged = Get-TestNames (New-UntaggedTestBinary $package) $package
            $known = @($Plan.Tests | Where-Object { $_.Command.Package -eq $package } | ForEach-Object { $_.Test }) +
            @($Plan.Harmless | Where-Object { $_.Package -eq $package -or $_.Package -eq '*' } | ForEach-Object { $_.Test })
            foreach ($name in ($names | Where-Object { $untagged -notcontains $_ })) {
                if ($known -notcontains $name) { $problems.Add("$name is a test of $package that exists only with the tag $TestBinaryTags, and the page lists it neither as a test nor as one that changes nothing.") }
            }
        }
    }
    if ($problems.Count -eq 0) {
        Write-Line ('The table is consistent: {0} groups, {1} tests, every test exists in the binary built from this tree.' -f $Plan.Groups.Count, $Plan.Tests.Count) 'Green'
        return $ExitPassed
    }
    $problems | ForEach-Object { Write-Line ('  ' + $_) 'Red' }
    return $ExitIncomplete
}

# --- Self test -------------------------------------------------------------------------------------------------------

function Invoke-SelfTest {
    $script:checks = 0
    $script:failures = 0
    function Assert-That([bool] $Condition, [string] $What) {
        $script:checks++
        if (-not $Condition) { $script:failures++; Write-Line ('  FAILED: ' + $What) 'Red' }
    }

    $cells = Split-TableRow '| `a \| b` | c |'
    Assert-That ($cells.Count -eq 2 -and $cells[0] -eq '`a | b`') 'an escaped pipe stays inside its cell'

    $command = ConvertTo-CommandPlan '`$env:PLAITWAY_ROOT_TESTS=''1''; go test -tags rootintegration -count=1 -run ''^TestRootX$'' -v ./internal/wg`'
    Assert-That ($command.Package -eq './internal/wg' -and $command.Pattern -eq '^TestRootX$' -and $command.Tags -eq 'rootintegration') 'the command is read into package, pattern and tags'
    Assert-That ($command.Environment['PLAITWAY_ROOT_TESTS'] -eq '1') 'the environment assignment is read'
    $threw = $false
    try { [void] (ConvertTo-CommandPlan '`go test -tags rootintegration -v ./internal/wg`') } catch { $threw = $true }
    Assert-That $threw 'a command without -run is refused'

    $pass = @('=== RUN   TestRootX', '--- PASS: TestRootX (0.01s)', 'PASS')
    Assert-That ((Get-RowVerdict 'TestRootX' 0 $pass) -eq $VerdictPassed) 'a pass line and exit 0 is a pass'
    Assert-That ((Get-RowVerdict 'TestRootX' 1 $pass) -eq $VerdictFailed) 'a pass line with exit 1 is a failure'
    Assert-That ((Get-RowVerdict 'TestRootX' 0 @('--- FAIL: TestRootX (0.01s)')) -eq $VerdictFailed) 'a fail line is a failure'
    $skip = @('=== RUN   TestRootX', '    x_test.go:9: needs an elevated shell', '--- SKIP: TestRootX (0.00s)', 'PASS')
    Assert-That ((Get-RowVerdict 'TestRootX' 0 $skip) -eq $VerdictSkipped) 'a skipped test is not a pass'
    Assert-That ((Get-SkipReason 'TestRootX' $skip) -eq 'x_test.go:9: needs an elevated shell') 'the reason of a skip is the line before it'
    Assert-That ((Get-RowVerdict 'TestRootX' 0 @('testing: warning: no tests to run', 'PASS')) -eq $VerdictNotRun) 'a pattern that matched nothing is not a pass'
    Assert-That ((Get-RowVerdict 'TestRootX' 0 @('--- PASS: TestRootXY (0.01s)')) -eq $VerdictNotRun) 'the pass of another test is not a pass'
    Assert-That ((Get-RowVerdict 'TestRootX' 0 @('    --- PASS: TestRootX (0.01s)')) -eq $VerdictNotRun) 'a subtest line is not the test'

    Assert-That (Test-ScratchPrefix '192.0.2.0/24') 'TEST-NET-1 is scratch'
    Assert-That (Test-ScratchPrefix '198.51.100.77/32') 'a host in TEST-NET-2 is scratch'
    Assert-That (Test-ScratchPrefix '203.0.113.128/25') 'half of TEST-NET-3 is scratch'
    Assert-That (Test-ScratchPrefix '2001:db8:ffff::77/128') 'the IPv6 documentation range is scratch'
    Assert-That (-not (Test-ScratchPrefix '192.0.0.0/16')) 'a range that contains TEST-NET-1 is not scratch'
    Assert-That (-not (Test-ScratchPrefix '192.168.1.0/24')) 'a private network is not scratch'
    Assert-That (-not (Test-ScratchPrefix '0.0.0.0/0')) 'the default route is not scratch'

    $before = [ordered]@{ Routes = @('0.0.0.0/0 via 192.168.1.1 on Ethernet'); Adapters = @('Ethernet | Intel'); Loopback = @('address 127.0.0.1/8'); RealService = @('absent'); Drivers = @('netvsc.inf_amd64_x'); Firewall = @(); OwnerOpenVpn = @('process openvpn pid 7'); Services = @('Dnscache'); DnsRules = @() }
    $after = [ordered]@{
        Routes       = @('0.0.0.0/0 via 192.168.1.1 on Ethernet', '198.51.100.0/24 via 0.0.0.0 on Loopback Pseudo-Interface 1', '10.9.0.0/16 via 192.168.1.9 on Ethernet')
        Adapters     = @('Ethernet | Intel', 'Plaitway-test-x | Wintun Userspace Tunnel')
        Loopback     = @('address 127.0.0.1/8', 'address 192.0.2.77/32')
        RealService  = @('Running Automatic')
        Drivers      = @('netvsc.inf_amd64_x', 'wintun.inf_amd64_y')
        Firewall     = @('{1} :: App=C:\x\wg.test.exe|Action=Allow')
        OwnerOpenVpn = @()
        Services     = @('Dnscache', 'PlaitwayHelperTest')
        DnsRules     = @('Plaitway-0123456789abcdef-1-0')
    }
    $findings = Compare-HostSnapshot $before $after
    function Assert-Severity([string] $Category, [string] $Fragment, [string] $Expected, [string] $What) {
        $matching = @($findings | Where-Object { $_.Category -eq $Category -and $_.Item -like "*$Fragment*" })
        Assert-That ($matching.Count -eq 1 -and $matching[0].Severity -eq $Expected) $What
    }
    Assert-Severity 'Adapters' 'Plaitway-test-x' $SeverityLeftover 'a Plaitway-* adapter is left behind'
    Assert-Severity 'Routes' '198.51.100.0/24' $SeverityLeftover 'a scratch route is left behind'
    Assert-Severity 'Routes' '10.9.0.0/16' $SeverityChanged 'a route outside the scratch ranges is a change, not a failure'
    Assert-Severity 'Loopback' '192.0.2.77' $SeverityLeftover 'a loopback address is left behind'
    Assert-Severity 'RealService' 'Running' $SeverityLeftover 'a changed PlaitwayHelper is a finding'
    Assert-Severity 'Drivers' 'wintun.inf' $SeverityNote 'a driver package is a documented side effect'
    Assert-Severity 'Firewall' 'wg.test.exe' $SeverityLeftover 'a firewall rule for a test binary is left behind'
    Assert-Severity 'OwnerOpenVpn' 'pid 7' $SeverityLeftover 'an OpenVPN process of the owner that ended is a finding'
    Assert-Severity 'Services' 'PlaitwayHelperTest' $SeverityLeftover 'the test service is left behind'
    Assert-Severity 'DnsRules' 'Plaitway-' $SeverityLeftover 'a Plaitway-* NRPT rule is left behind'
    $same = Compare-HostSnapshot $before $before
    Assert-That (@($same).Count -eq 0) 'two equal snapshots have no difference'

    $recovery = (Get-RecoveryCommands $findings) -join "`n"
    Assert-That ($recovery -match 'Remove-NetRoute -DestinationPrefix 198\.51\.100\.0/24') 'the route comes with its removal command'
    Assert-That ($recovery -match 'Remove-NetIPAddress .* 192\.0\.2\.77') 'the loopback address comes with its removal command'
    Assert-That ($recovery -match 'sc\.exe delete PlaitwayHelperTest') 'the test service comes with its removal command'
    Assert-That ($recovery -match 'Where-Object PSChildName -like ''Plaitway-\*'' \| Remove-Item; Clear-DnsClientCache') 'the NRPT rule comes with the sweep command'
    Assert-That ($recovery -notmatch '10\.9\.0\.0') 'a change that is not a leftover gets no command'

    Write-Line ('Self test: {0} checks, {1} failed.' -f $script:checks, $script:failures) $(if ($script:failures -eq 0) { 'Green' } else { 'Red' })
    return $(if ($script:failures -eq 0) { $ExitPassed } else { $ExitIncomplete })
}

# --- Main ----------------------------------------------------------------------------------------------------------

function Write-Refusal([string[]] $Reasons) {
    Write-Line 'Refusing to run:' 'Red'
    $Reasons | ForEach-Object { Write-Line ('  ' + $_) 'Red' }
    return $ExitRefused
}

function Invoke-Run($Plan, $Selected) {
    if (-not (Test-Elevated)) {
        if (-not $AllowUnelevated) { return Write-Refusal @('This changes the machine and needs administrator rights: run it from an elevated PowerShell. -WhatIf shows the plan without elevation.') }
        Write-Line 'Not elevated: every test refuses or skips before it changes anything. This only checks the script.' 'Yellow'
    }
    $reasons = @(Get-ToolProblems)
    $unmerged = @(Get-UnmergedFiles)
    if ($unmerged.Count -gt 0) { $reasons += "The tree has unmerged files: $($unmerged -join '; ')" }
    foreach ($group in $Selected) { $reasons += @(Get-GroupRefusals $Plan $group) }
    if ($reasons.Count -gt 0) { return Write-Refusal $reasons }

    $script:LogDirectory = Join-Path $OutputDirectory 'logs'
    $null = New-Item -ItemType Directory -Force -Path $script:LogDirectory
    $script:LogPath = Join-Path $script:LogDirectory ('elevated-tests-{0:yyyyMMdd-HHmmss}.log' -f (Get-Date))
    Write-Line ('Plaitway elevated tests, {0}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'))
    Write-Line ('Log      ' + $script:LogPath)
    Write-Line ('Tree     ' + (Get-TreeSummary))
    Write-Line ('Windows  ' + [Environment]::OSVersion.VersionString)
    Show-Switches

    $go = Invoke-NativeLines 'go' @('build', './cmd/...', './internal/...') $RepositoryRoot $false
    if ($go.ExitCode -ne 0) {
        $go.Output | Select-Object -Last 30 | ForEach-Object { Write-Line ('  ' + $_) 'Red' }
        return Write-Refusal @('The daemon, the client and the packages below internal do not build (go build ./cmd/... ./internal/...); the tests are built from them.')
    }

    $selectedTests = @($Selected | ForEach-Object { Get-GroupTests $Plan $_ } | Where-Object { Test-TestRunnable $_ })
    $binaries = New-TestBinaries $selectedTests
    $watcherPath = Join-Path $OutputDirectory 'loopbackwatch.exe'
    $watcherBuild = Invoke-NativeLines 'go' @('build', '-o', $watcherPath, $WatcherPackage) $RepositoryRoot $false
    if ($watcherBuild.ExitCode -ne 0) { return Write-Refusal @('The socket watcher did not build (scripts/windows/loopbackwatch).') }

    $baseline = Get-HostSnapshot
    $results = New-Object System.Collections.Generic.List[object]
    foreach ($group in $Selected) {
        Show-GroupPlan $Plan $group
        if (-not (Confirm-Group $group)) {
            $results.Add([pscustomobject]@{ Group = $group.Name; Declined = $true; Rows = @(); Findings = @(); Sockets = @(); GuardsActed = @(); Leftovers = $false })
            continue
        }
        $result = Invoke-Group $Plan $group $binaries $watcherPath
        $results.Add($result)
        if ($result.Leftovers) {
            Write-Line ''
            Write-Line 'Stopping: the machine is not as it was. Undo what is listed above, then run the remaining groups again.' 'Red'
            break
        }
    }

    Write-Heading 'The whole run, against the machine before the first group'
    $overall = @(Compare-HostSnapshot $baseline (Get-HostSnapshot))
    if ($overall.Count -eq 0) { Write-Line '  no difference' 'Green' } else { Show-Findings $overall }
    Show-Summary $results
    $exitCode = Get-ExitCode $results
    Write-Line ''
    Write-Line ('Exit code {0}. The log is {1}' -f $exitCode, $script:LogPath)
    return $exitCode
}

function Invoke-Main {
    if ($SelfTest) { return (Invoke-SelfTest) }
    $plan = Read-TestPlan $TablePath
    $problems = @(Test-PlanConsistency $plan)
    if ($problems.Count -gt 0) { return (Write-Refusal (@("The table of tests is wrong ($TablePath):") + $problems)) }
    if ($CheckTable) { return (Invoke-CheckTable $plan) }
    if ($SnapshotOnly) { Invoke-SnapshotOnly; return $ExitPassed }
    try { $selected = @(Resolve-SelectedGroups $plan) } catch { return (Write-Refusal @($_.Exception.Message)) }
    if ($WhatIfPreference) { Show-DryRun $plan $selected; return $ExitPassed }
    return (Invoke-Run $plan $selected)
}

$exitCode = Invoke-Main
exit ([int] @($exitCode)[-1])
