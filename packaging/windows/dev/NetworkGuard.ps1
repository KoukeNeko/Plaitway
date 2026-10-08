<#
.SYNOPSIS
A dead-man's switch for network changes: records the network state, and puts it back when a timer ends or the
network stops working, whatever happened to the process that made the change.

.DESCRIPTION
Arm it before anything that touches routes, DNS, adapters or interface metrics:

    .\NetworkGuard.ps1 -Arm -Minutes 6          # snapshot, then start the watcher; prints the guard id
    ... the change, the test, the run ...
    .\NetworkGuard.ps1 -Status                  # what the watcher is doing
    .\NetworkGuard.ps1 -Extend -Minutes 6       # a longer run: moves the deadline
    .\NetworkGuard.ps1 -Disarm                  # all is well: the watcher ends and puts nothing back
    .\NetworkGuard.ps1 -Revert                  # put the state back now and end the watcher

The watcher is a separate process that is not a child of the shell that armed it (it is created through WMI, which
also takes it out of the job object a terminal or an agent may kill with its children), so closing the session, a
crash or a hung test does not stop it. It puts the state back when

  * the deadline passes and nobody disarmed it, or
  * the network probe fails three times in a row, ten seconds apart, after it worked when the guard was armed (the
    probe is a TCP connect to the default gateway and one to 1.1.1.1:443; the guard acts only when both fail).

What it puts back is deliberately narrow, because it runs as an administrator:

  * a default route (0.0.0.0/0, ::/0) of the snapshot that is gone;
  * the DNS servers and the interface metric of every adapter of the snapshot that changed;
  * routes that are new and are clearly a test's: inside the documentation ranges 192.0.2.0/24, 198.51.100.0/24,
    203.0.113.0/24, 2001:db8::/32, or on an adapter named Plaitway-*; it removes those and leaves every other new route
    alone, listed in the log;
  * NRPT rules named Plaitway-* that the snapshot did not have, then the DNS cache.

It never deletes an adapter, a service, a driver or a firewall rule: those are the cleanup of the tests and of their
own guards. Its log is <StateDirectory>\<Name>.log.

.PARAMETER Name
The guard's name: the snapshot, the log and the control files are named after it. Default: guard.

.PARAMETER Minutes
How long until the deadline. Default 5.

.PARAMETER StateDirectory
Where the snapshot, the log and the control files live. Default: %TEMP%\plaitway-network-guard.
#>
[CmdletBinding(DefaultParameterSetName = 'Status')]
param(
    [Parameter(ParameterSetName = 'Arm', Mandatory)] [switch] $Arm,
    [Parameter(ParameterSetName = 'Disarm', Mandatory)] [switch] $Disarm,
    [Parameter(ParameterSetName = 'Status')] [switch] $Status,
    [Parameter(ParameterSetName = 'Extend', Mandatory)] [switch] $Extend,
    [Parameter(ParameterSetName = 'Revert', Mandatory)] [switch] $Revert,
    [Parameter(ParameterSetName = 'Watch', Mandatory)] [switch] $Watch,
    [Parameter(ParameterSetName = 'SelfTest', Mandatory)] [switch] $SelfTest,
    [string] $Name = 'guard',
    [ValidateRange(1, 240)] [int] $Minutes = 5,
    [string] $StateDirectory = (Join-Path $env:TEMP 'plaitway-network-guard')
)

$ErrorActionPreference = 'Stop'

$NrptKey = 'HKLM:\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig'
$NrptPrefix = 'Plaitway-'
$ScratchPrefixes = @('192.0.2.', '198.51.100.', '203.0.113.', '2001:db8:')
$AdapterPrefix = 'Plaitway'
$PollSeconds = 5
$ProbeSeconds = 10
$ProbeFailuresToAct = 3
$ProbeTimeoutMilliseconds = 2000
$InternetProbeAddress = '1.1.1.1'
$InternetProbePort = 443

function Get-Paths([string] $GuardName) {
    [pscustomobject]@{
        Snapshot = Join-Path $StateDirectory "$GuardName.snapshot.json"
        Log      = Join-Path $StateDirectory "$GuardName.log"
        Deadline = Join-Path $StateDirectory "$GuardName.deadline"
        Disarm   = Join-Path $StateDirectory "$GuardName.disarm"
        Revert   = Join-Path $StateDirectory "$GuardName.revert"
        Pid      = Join-Path $StateDirectory "$GuardName.pid"
        FailProbe = Join-Path $StateDirectory "$GuardName.failprobe"
        Done     = Join-Path $StateDirectory "$GuardName.done"
    }
}

function Write-Log([string] $LogFile, [string] $Text) {
    Add-Content -Path $LogFile -Value ('{0}  {1}' -f (Get-Date -Format 'HH:mm:ss'), $Text) -Encoding UTF8
}

# ---------------------------------------------------------------------------------------------- the state

function Get-AdapterName([int] $Index, $Adapters) {
    $match = $Adapters | Where-Object { $_.Index -eq $Index } | Select-Object -First 1
    if ($match) { return $match.Name }
    return ''
}

function Get-NetworkState {
    [pscustomobject]@{
        Taken      = (Get-Date).ToString('o')
        Routes     = @(Get-NetRoute -ErrorAction SilentlyContinue | ForEach-Object {
                [pscustomobject]@{ Prefix = $_.DestinationPrefix; NextHop = $_.NextHop; Index = [int] $_.InterfaceIndex; Metric = [int] $_.RouteMetric; Family = [string] $_.AddressFamily } })
        Dns        = @(Get-DnsClientServerAddress -ErrorAction SilentlyContinue | ForEach-Object {
                [pscustomobject]@{ Index = [int] $_.InterfaceIndex; Family = [string] $_.AddressFamily; Servers = @($_.ServerAddresses) } })
        Nrpt       = @(Get-ChildItem $NrptKey -ErrorAction SilentlyContinue | ForEach-Object { $_.PSChildName })
        Interfaces = @(Get-NetIPInterface -ErrorAction SilentlyContinue | ForEach-Object {
                [pscustomobject]@{ Index = [int] $_.InterfaceIndex; Family = [string] $_.AddressFamily; Metric = [int] $_.InterfaceMetric; Automatic = [string] $_.AutomaticMetric } })
        Adapters   = @(Get-NetAdapter -IncludeHidden -ErrorAction SilentlyContinue | ForEach-Object {
                [pscustomobject]@{ Name = $_.Name; Index = [int] $_.InterfaceIndex; Status = [string] $_.Status } })
    }
}

function Test-SameRoute($A, $B) { $A.Prefix -eq $B.Prefix -and $A.NextHop -eq $B.NextHop -and $A.Index -eq $B.Index }

function Test-DefaultPrefix([string] $Prefix) { $Prefix -eq '0.0.0.0/0' -or $Prefix -eq '::/0' }

function Test-ScratchRoute($Route, $Adapters) {
    foreach ($prefix in $ScratchPrefixes) { if ($Route.Prefix.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { return $true } }
    return (Get-AdapterName $Route.Index $Adapters).StartsWith($AdapterPrefix, [StringComparison]::OrdinalIgnoreCase)
}

# What to do to get from $Now back to $Before. A list of actions, so that a self test can check the decisions without
# touching the machine.
function Get-RestorePlan($Before, $Now) {
    $plan = New-Object System.Collections.Generic.List[object]

    foreach ($lost in $Before.Routes | Where-Object { (Test-DefaultPrefix $_.Prefix) -and $_.NextHop -notin @('0.0.0.0', '::') }) {
        if (-not ($Now.Routes | Where-Object { Test-SameRoute $_ $lost })) { $plan.Add([pscustomobject]@{ Action = 'AddRoute'; Route = $lost }) }
    }
    foreach ($route in $Now.Routes) {
        if ($Before.Routes | Where-Object { Test-SameRoute $_ $route }) { continue }
        if (Test-ScratchRoute $route $Now.Adapters) { $plan.Add([pscustomobject]@{ Action = 'RemoveRoute'; Route = $route }) }
        else { $plan.Add([pscustomobject]@{ Action = 'Note'; Text = "new route left alone: $($route.Prefix) via $($route.NextHop) on $($route.Index)" }) }
    }
    foreach ($was in $Before.Dns) {
        $is = $Now.Dns | Where-Object { $_.Index -eq $was.Index -and $_.Family -eq $was.Family } | Select-Object -First 1
        if ($is -and (($is.Servers -join ',') -ne ($was.Servers -join ','))) { $plan.Add([pscustomobject]@{ Action = 'SetDns'; Dns = $was }) }
    }
    foreach ($was in $Before.Interfaces) {
        $is = $Now.Interfaces | Where-Object { $_.Index -eq $was.Index -and $_.Family -eq $was.Family } | Select-Object -First 1
        if ($is -and ($is.Metric -ne $was.Metric -or $is.Automatic -ne $was.Automatic)) { $plan.Add([pscustomobject]@{ Action = 'SetMetric'; Interface = $was }) }
    }
    foreach ($rule in $Now.Nrpt | Where-Object { $_.StartsWith($NrptPrefix, [StringComparison]::OrdinalIgnoreCase) -and $_ -notin $Before.Nrpt }) {
        $plan.Add([pscustomobject]@{ Action = 'RemoveNrpt'; Rule = $rule })
    }
    return $plan.ToArray()
}

function Invoke-RestorePlan($Plan, [string] $LogFile) {
    foreach ($step in $Plan) {
        try {
            switch ($step.Action) {
                'AddRoute' {
                    New-NetRoute -DestinationPrefix $step.Route.Prefix -NextHop $step.Route.NextHop -InterfaceIndex $step.Route.Index -RouteMetric $step.Route.Metric -Confirm:$false | Out-Null
                    Write-Log $LogFile "put back the route $($step.Route.Prefix) via $($step.Route.NextHop) on $($step.Route.Index)"
                }
                'RemoveRoute' {
                    Remove-NetRoute -DestinationPrefix $step.Route.Prefix -NextHop $step.Route.NextHop -InterfaceIndex $step.Route.Index -Confirm:$false
                    Write-Log $LogFile "removed the scratch route $($step.Route.Prefix) via $($step.Route.NextHop) on $($step.Route.Index)"
                }
                'SetDns' {
                    if ($step.Dns.Servers.Count -gt 0) { Set-DnsClientServerAddress -InterfaceIndex $step.Dns.Index -ServerAddresses $step.Dns.Servers }
                    else { Set-DnsClientServerAddress -InterfaceIndex $step.Dns.Index -ResetServerAddresses }
                    Write-Log $LogFile "put back the DNS servers of interface $($step.Dns.Index) ($($step.Dns.Family)): $($step.Dns.Servers -join ',')"
                }
                'SetMetric' {
                    if ($step.Interface.Automatic -eq 'Enabled') { Set-NetIPInterface -InterfaceIndex $step.Interface.Index -AddressFamily $step.Interface.Family -AutomaticMetric Enabled }
                    else { Set-NetIPInterface -InterfaceIndex $step.Interface.Index -AddressFamily $step.Interface.Family -InterfaceMetric $step.Interface.Metric }
                    Write-Log $LogFile "put back the metric of interface $($step.Interface.Index) ($($step.Interface.Family))"
                }
                'RemoveNrpt' {
                    Remove-Item -Path (Join-Path $NrptKey $step.Rule) -Recurse -Force
                    Write-Log $LogFile "removed the NRPT rule $($step.Rule)"
                }
                'Note' { Write-Log $LogFile $step.Text }
            }
        }
        catch { Write-Log $LogFile "FAILED $($step.Action): $($_.Exception.Message)" }
    }
    if ($Plan | Where-Object { $_.Action -eq 'RemoveNrpt' -or $_.Action -eq 'SetDns' }) { try { Clear-DnsClientCache } catch { } }
}

function Restore-Network([string] $Reason, $Paths) {
    Write-Log $Paths.Log "RESTORING: $Reason"
    $before = Get-Content $Paths.Snapshot -Raw | ConvertFrom-Json
    $plan = @(Get-RestorePlan $before (Get-NetworkState))
    Write-Log $Paths.Log "the plan has $(@($plan).Count) step(s)"
    Invoke-RestorePlan $plan $Paths.Log
    $left = Get-RestorePlan $before (Get-NetworkState) | Where-Object { $_.Action -ne 'Note' }
    Write-Log $Paths.Log $(if (@($left).Count -eq 0) { 'restored: the state matches the snapshot' } else { "NOT restored: $(@($left).Count) step(s) still differ: $(($left | ForEach-Object { $_.Action }) -join ', ')" })
}

# ------------------------------------------------------------------------------------------------- the probe

function Test-Tcp([string] $Address, [int] $Port) {
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $task = $client.ConnectAsync($Address, $Port)
        return $task.Wait($ProbeTimeoutMilliseconds) -and $client.Connected
    }
    catch { return $false }
    finally { $client.Dispose() }
}

function Get-GatewayAddress {
    $route = Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Sort-Object RouteMetric | Select-Object -First 1
    if ($route -and $route.NextHop -ne '0.0.0.0') { return $route.NextHop }
    return $null
}

# The network works when the gateway answers on any port (a refusal is an answer) or the internet probe connects.
function Test-NetworkWorks {
    $gateway = Get-GatewayAddress
    if ($gateway) {
        foreach ($port in 443, 80, 53) { if (Test-Tcp $gateway $port) { return $true } }
        if (Test-Connection -TargetName $gateway -Count 1 -TimeoutSeconds 2 -Quiet -ErrorAction SilentlyContinue) { return $true }
    }
    return Test-Tcp $InternetProbeAddress $InternetProbePort
}

# --------------------------------------------------------------------------------------------- the watcher

function Invoke-Watch([string] $GuardName) {
    $paths = Get-Paths $GuardName
    Write-Log $paths.Log "watcher started, pid $PID"
    $baselineWorks = [bool] ((Get-Content $paths.Snapshot -Raw | ConvertFrom-Json).NetworkWorked)
    Write-Log $paths.Log "the network worked when the guard was armed: $baselineWorks"
    $failures = 0
    $nextProbe = Get-Date
    while ($true) {
        if (Test-Path $paths.Disarm) { Write-Log $paths.Log 'disarmed: nothing is put back'; break }
        if (Test-Path $paths.Revert) { Restore-Network 'asked to' $paths; break }
        $deadline = [datetime]::Parse((Get-Content $paths.Deadline -Raw).Trim(), $null, 'RoundtripKind')
        if ((Get-Date) -ge $deadline) { Restore-Network 'the deadline passed and the guard was not disarmed' $paths; break }
        if ($baselineWorks -and (Get-Date) -ge $nextProbe) {
            $nextProbe = (Get-Date).AddSeconds($ProbeSeconds)
            # <name>.failprobe makes the probe fail: the test of the early revert, which cannot break the network on purpose.
            if (-not (Test-Path $paths.FailProbe) -and (Test-NetworkWorks)) { $failures = 0 }
            else {
                $failures++
                Write-Log $paths.Log "network probe failed ($failures of $ProbeFailuresToAct)"
                if ($failures -ge $ProbeFailuresToAct) { Restore-Network 'the network stopped working' $paths; break }
            }
        }
        Start-Sleep -Seconds $PollSeconds
    }
    New-Item -Path $paths.Done -ItemType File -Force | Out-Null
    Write-Log $paths.Log 'watcher ended'
}

# ------------------------------------------------------------------------------------------- the commands

function Test-Elevated {
    ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-WatcherProcessId($Paths) {
    if (-not (Test-Path $Paths.Pid)) { return $null }
    $watcherPid = [int] (Get-Content $Paths.Pid -Raw)
    if (Get-Process -Id $watcherPid -ErrorAction SilentlyContinue) { return $watcherPid }
    return $null
}

function Start-Guard {
    if (-not (Test-Elevated)) { throw 'the guard puts routes and DNS back, which needs an elevated shell' }
    New-Item -ItemType Directory -Force $StateDirectory | Out-Null
    $paths = Get-Paths $Name
    if (Get-WatcherProcessId $paths) { throw "a guard named '$Name' is already watching (pid $(Get-WatcherProcessId $paths)); disarm or revert it first" }
    foreach ($file in $paths.Disarm, $paths.Revert, $paths.Done, $paths.Log) { if (Test-Path $file) { [IO.File]::Delete($file) } }

    $state = Get-NetworkState
    $state | Add-Member -NotePropertyName NetworkWorked -NotePropertyValue (Test-NetworkWorks)
    $state | ConvertTo-Json -Depth 6 | Set-Content -Path $paths.Snapshot -Encoding UTF8
    (Get-Date).AddMinutes($Minutes).ToString('o') | Set-Content -Path $paths.Deadline -Encoding ASCII

    $pwsh = (Get-Command pwsh).Source
    $command = '"{0}" -NoProfile -ExecutionPolicy Bypass -File "{1}" -Watch -Name "{2}" -StateDirectory "{3}"' -f $pwsh, $PSCommandPath, $Name, $StateDirectory
    $result = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $command }
    if ($result.ReturnValue -ne 0) { throw "the watcher did not start (WMI return value $($result.ReturnValue))" }
    $result.ProcessId | Set-Content -Path $paths.Pid -Encoding ASCII

    # The watcher has to be alive and have written its first line before the caller changes anything.
    $end = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $end -and -not (Test-Path $paths.Log)) { Start-Sleep -Milliseconds 200 }
    if (-not (Test-Path $paths.Log) -or -not (Get-Process -Id $result.ProcessId -ErrorAction SilentlyContinue)) { throw "the watcher is not running; see $($paths.Log)" }
    "armed '$Name': watcher pid $($result.ProcessId), restores at $((Get-Date).AddMinutes($Minutes).ToString('HH:mm:ss')) unless disarmed; the network worked at arming: $($state.NetworkWorked); log $($paths.Log)"
}

function Stop-Guard([string] $Control) {
    $paths = Get-Paths $Name
    if (-not (Get-WatcherProcessId $paths)) { throw "no guard named '$Name' is watching" }
    New-Item -Path $paths.$Control -ItemType File -Force | Out-Null
    $end = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $end -and -not (Test-Path $paths.Done)) { Start-Sleep -Milliseconds 300 }
    if (-not (Test-Path $paths.Done)) { throw "the watcher did not finish within 60 s; see $($paths.Log)" }
    Get-Content $paths.Log -Tail 6
}

function Show-Status {
    $paths = Get-Paths $Name
    $watcherPid = Get-WatcherProcessId $paths
    if (-not $watcherPid) { "no guard named '$Name' is watching"; if (Test-Path $paths.Log) { Get-Content $paths.Log -Tail 4 }; return }
    $deadline = [datetime]::Parse((Get-Content $paths.Deadline -Raw).Trim(), $null, 'RoundtripKind')
    "guard '$Name' is watching (pid $watcherPid); restores at $($deadline.ToString('HH:mm:ss')), in $([math]::Round(($deadline - (Get-Date)).TotalSeconds)) s"
    Get-Content $paths.Log -Tail 4
}

function Invoke-SelfTest {
    $failures = New-Object System.Collections.Generic.List[string]
    function Check([bool] $Ok, [string] $What) { if (-not $Ok) { $failures.Add($What) } }
    function Route($Prefix, $NextHop, $Index) { [pscustomobject]@{ Prefix = $Prefix; NextHop = $NextHop; Index = $Index; Metric = 5; Family = 'IPv4' } }
    $adapters = @([pscustomobject]@{ Name = 'Ethernet'; Index = 2; Status = 'Up' }, [pscustomobject]@{ Name = 'Plaitway-test-wg'; Index = 40; Status = 'Up' })
    $before = [pscustomobject]@{
        Routes = @(Route '0.0.0.0/0' '192.168.1.1' 2; Route '192.168.1.0/24' '0.0.0.0' 2)
        Dns = @([pscustomobject]@{ Index = 2; Family = 'IPv4'; Servers = @('192.168.1.1') })
        Nrpt = @(); Adapters = $adapters
        Interfaces = @([pscustomobject]@{ Index = 2; Family = 'IPv4'; Metric = 25; Automatic = 'Enabled' })
    }
    $quiet = @(Get-RestorePlan $before $before)
    Check (@($quiet).Count -eq 0) 'an unchanged machine needs no step'

    $now = [pscustomobject]@{
        Routes = @(Route '192.168.1.0/24' '0.0.0.0' 2; Route '192.0.2.0/24' '0.0.0.0' 1; Route '198.51.100.9/32' '0.0.0.0' 40; Route '10.9.8.0/24' '10.0.0.1' 2; Route '0.0.0.0/1' '0.0.0.0' 40)
        Dns = @([pscustomobject]@{ Index = 2; Family = 'IPv4'; Servers = @('127.0.0.1') })
        Nrpt = @('Plaitway-abc-1-0', 'SomeoneElse-rule')
        Interfaces = @([pscustomobject]@{ Index = 2; Family = 'IPv4'; Metric = 76; Automatic = 'Disabled' })
        Adapters = $adapters
    }
    $plan = @(Get-RestorePlan $before $now)
    $actions = $plan | ForEach-Object { $_.Action }
    Check ($actions -contains 'AddRoute' -and ($plan | Where-Object { $_.Action -eq 'AddRoute' }).Route.Prefix -eq '0.0.0.0/0') 'a lost default route is put back'
    $removed = @($plan | Where-Object { $_.Action -eq 'RemoveRoute' } | ForEach-Object { $_.Route.Prefix })
    Check ($removed -contains '192.0.2.0/24' -and $removed -contains '198.51.100.9/32' -and $removed -contains '0.0.0.0/1') 'routes of the documentation ranges and of a Plaitway adapter are removed'
    Check ($removed -notcontains '10.9.8.0/24') 'a foreign new route is not removed'
    Check (@($plan | Where-Object { $_.Action -eq 'Note' -and $_.Text -match '10.9.8.0/24' }).Count -eq 1) 'a foreign new route is noted'
    Check (@($plan | Where-Object { $_.Action -eq 'SetDns' -and ($_.Dns.Servers -join ',') -eq '192.168.1.1' }).Count -eq 1) 'a changed DNS server list is put back'
    Check (@($plan | Where-Object { $_.Action -eq 'SetMetric' -and $_.Interface.Automatic -eq 'Enabled' }).Count -eq 1) 'a changed interface metric is put back'
    $rules = @($plan | Where-Object { $_.Action -eq 'RemoveNrpt' } | ForEach-Object { $_.Rule })
    Check ($rules.Count -eq 1 -and $rules[0] -eq 'Plaitway-abc-1-0') ('only Plaitway NRPT rules are removed (got: ' + ($rules -join ', ') + ')')
    Check (-not (Test-DefaultPrefix '10.0.0.0/8') -and (Test-DefaultPrefix '::/0')) 'default prefixes are recognised'

    if ($failures.Count -eq 0) { 'self test: all checks passed'; return }
    $failures | ForEach-Object { "self test FAILED: $_" }
    exit 1
}

switch ($PSCmdlet.ParameterSetName) {
    'Arm'      { Start-Guard }
    'Disarm'   { Stop-Guard 'Disarm' }
    'Revert'   { Stop-Guard 'Revert' }
    'Extend'   {
        $paths = Get-Paths $Name
        if (-not (Get-WatcherProcessId $paths)) { throw "no guard named '$Name' is watching" }
        (Get-Date).AddMinutes($Minutes).ToString('o') | Set-Content -Path $paths.Deadline -Encoding ASCII
        Show-Status
    }
    'Status'   { Show-Status }
    'Watch'    { Invoke-Watch $Name }
    'SelfTest' { Invoke-SelfTest }
}
