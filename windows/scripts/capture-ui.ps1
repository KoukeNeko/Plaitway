# Starts the Go daemon on its fake backend and the WinUI app against it, goes through the pages of the app in each theme and
# language, and leaves a screenshot and the UI Automation tree of every page in -ArtifactsDirectory.
#
#   pwsh windows\scripts\capture-ui.ps1 -ArtifactsDirectory <folder> [-Themes light,dark] [-Languages en-US,zh-TW] [-Build]
#
# It touches nothing outside its own scratch folder: no route, no DNS, no service, no startup entry, no elevation. The window
# appears on the screen of whoever runs it, on top, for about half a minute per theme and language. Buttons and tabs are pressed through UI
# Automation; the few keys it sends (Up and Enter in the tray menu, to reach the question before quitting) are sent only while the
# app has the keyboard, so none can land in another program. The daemon is
# plaitwayd -fake, on a pipe of its own; the app is a Debug build, because only a Debug build follows PLAITWAY_SOCKET.
#
# Files, for each theme and language: <page>-<theme>-<language>.png (the window), <page>-<theme>-<language>.txt (its UI
# Automation tree and the controls that have no name), and summary.txt for the run. The exit code is 1 when a control that
# needs a name for a screen reader has none. (That the app quits from its tray menu is a test: windows\Tests\Plaitway.App.UiTests.)

[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $ArtifactsDirectory,
    [ValidateSet('Debug', 'Release')] [string] $Configuration = 'Debug',
    [string[]] $Themes = @('light', 'dark'),
    [string[]] $Languages = @('en-US', 'zh-TW'),
    [string] $Bounds = '60,40,1280,860',
    [switch] $Build
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'UiAutomationHelpers.ps1')

$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$AppDirectory = Join-Path $RepositoryRoot "windows\Sources\Plaitway.App\bin\$Configuration\net10.0-windows10.0.19041.0\win-x64"
$ScratchDirectory = Join-Path ([IO.Path]::GetTempPath()) ('plaitway-ui-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
$Catalogs = @('macos\Sources\PlaitwayMenuBar\Resources\Localizable.xcstrings', 'windows\Resources\Windows.xcstrings') |
    ForEach-Object { Get-Content (Join-Path $RepositoryRoot $_) -Raw -Encoding utf8 | ConvertFrom-Json -AsHashtable }
$WmApp = 0x8000
$WmContextMenu = 0x7B
$WmCancelMode = 0x1F
$VkUp = 0x26
$VkReturn = 0x0D
$StartupSeconds = 25

New-Item -ItemType Directory -Force $ArtifactsDirectory | Out-Null
$processes = [System.Collections.Generic.List[object]]::new()
$summary = [System.Collections.Generic.List[string]]::new()
$failures = [System.Collections.Generic.List[string]]::new()

# The words of the catalogs, so that this script says nothing the app does not.
function Get-Text([string] $Key, [string] $Language) {
    foreach ($catalog in $Catalogs) {
        if ($catalog.strings.ContainsKey($Key)) {
            return $(if ($Language -eq 'en-US') { $Key } else { $catalog.strings[$Key].localizations['zh-Hant'].stringUnit.value })
        }
    }
    throw "no string '$Key' in the catalogs"
}

function Build-Go([string] $Package, [string] $Output) {
    $env:GOOS = $null; $env:GOARCH = $null
    Push-Location $RepositoryRoot
    try { & go build -o $Output $Package; if ($LASTEXITCODE -ne 0) { throw "go build $Package failed" } } finally { Pop-Location }
}

# A profile that fails on purpose makes the command fail; the state it leaves is what the run is after.
function Invoke-Cli([string] $Pipe, [string[]] $Arguments, [switch] $MayFail) {
    & $script:CliExe -socket $Pipe @Arguments *> $null
    if ($LASTEXITCODE -ne 0 -and -not $MayFail) { throw "plaitway $($Arguments -join ' ') failed" }
}

function Start-Daemon {
    $pipe = '\\.\pipe\plaitway-ui-' + [Guid]::NewGuid().ToString('N').Substring(0, 12)
    $log = Join-Path $ScratchDirectory ('daemon-' + [Guid]::NewGuid().ToString('N').Substring(0, 6) + '.log')
    $state = Join-Path $ScratchDirectory ('state-' + [Guid]::NewGuid().ToString('N').Substring(0, 6))
    $daemon = Start-Process $script:DaemonExe -PassThru -WindowStyle Hidden -RedirectStandardError $log -ArgumentList @(
        '-fake', '-socket', $pipe, '-state-dir', $state, '-log-file', '""', '-run-dir', (Join-Path $state 'run'))
    $processes.Add($daemon)
    Wait-Until 'the daemon to listen' { (Test-Path $log) -and ((Get-Content $log -Raw) -match 'listening') }
    return [pscustomobject]@{ Process = $daemon; Pipe = $pipe }
}

# The profiles every run looks at: two connected (one of each kind), one idle, one failed, and optionally one that asks for a password.
function Add-Profiles([string] $Pipe, [switch] $WithCredentials) {
    $certificate = "<ca>`n-----BEGIN CERTIFICATE-----`nMIIBtest`n-----END CERTIFICATE-----`n</ca>`n"
    $profiles = [ordered]@{
        'Office.ovpn' = "client`nremote vpn.example.net 1194`nroute 192.168.1.0 255.255.255.0`n$certificate"
        'Lab.ovpn'    = "client`nremote lab.example.net 443 tcp`n$certificate"
        'Home.conf'   = "[Interface]`nPrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`nAddress = 10.6.0.2/32`nDNS = 10.6.0.1`n`n[Peer]`nPublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=`nAllowedIPs = 0.0.0.0/0`nEndpoint = 203.0.113.5:51820`n"
        'Broken.ovpn' = "client`nremote broken.example.net 1194`n# fake: fail`n$certificate"
    }
    if ($WithCredentials) { $profiles['Router.ovpn'] = "client`nremote router.example.net 1194`n# fake: needs-credentials`n$certificate" }
    foreach ($name in $profiles.Keys) {
        $file = Join-Path $ScratchDirectory $name
        [IO.File]::WriteAllText($file, $profiles[$name], (New-Object Text.UTF8Encoding $false))
        Invoke-Cli $Pipe @('import', $file)
    }
    foreach ($name in 'Office', 'Home') { Invoke-Cli $Pipe @('connect', $name) }
    Invoke-Cli $Pipe @('connect', 'Broken') -MayFail
    if ($WithCredentials) { Invoke-Cli $Pipe @('connect', 'Router') -MayFail }
}

function Start-App([string] $Theme, [string] $Language, [string] $Pipe, [string] $Name) {
    $arguments = @('--bounds', $Bounds, '--topmost', '--theme', $Theme, '--language', $Language, '--data-dir', (Join-Path $ScratchDirectory "app-$Name"))
    $env:PLAITWAY_SOCKET = $Pipe
    $env:PLAITWAY_RUN_REPORT = Join-Path $ScratchDirectory "report-$Name.txt"
    try { $app = Start-Process (Join-Path $AppDirectory 'Plaitway.exe') -ArgumentList $arguments -PassThru }
    finally { $env:PLAITWAY_SOCKET = $null; $env:PLAITWAY_RUN_REPORT = $null }
    $processes.Add($app)
    Wait-Until 'the first window' { $app.Refresh(); $app.MainWindowHandle -ne [IntPtr]::Zero } $StartupSeconds
    return [pscustomobject]@{ Process = $app; Report = (Join-Path $ScratchDirectory "report-$Name.txt") }
}

# One page: screenshot it, write its tree, and count what has no name.
function Save-Page([string] $Page, $App, [string] $Suffix) {
    $window = $App.Process.MainWindowHandle
    $root = [System.Windows.Automation.AutomationElement]::FromHandle($window)
    Start-Sleep -Milliseconds 900
    Save-WindowScreenshot $window (Join-Path $ArtifactsDirectory "$Page-$Suffix.png")
    $tree = Get-AutomationTreeText $root
    $unnamed = @(Get-UnnamedControls $root)
    $output = @($tree) + @('', "controls that need a name and have none: $($unnamed.Count)") + @($unnamed | ForEach-Object { "  $_" })
    [IO.File]::WriteAllLines((Join-Path $ArtifactsDirectory "$Page-$Suffix.txt"), $output, (New-Object Text.UTF8Encoding $false))
    $summary.Add(('{0,-34} {1,4} controls, {2} without a name' -f "$Page-$Suffix", $tree.Count, $unnamed.Count))
    foreach ($item in $unnamed) { $summary.Add("    $item"); $failures.Add("$Page-${Suffix}: $item") }
}

function Select-ByName($Root, [string] $Name, [string] $ControlType) {
    Wait-Until "'$Name' ($ControlType)" { $null -ne (Find-ByName $Root $Name $ControlType) } 15
    Select-Element (Find-ByName $Root $Name $ControlType)
}

function Select-Profile($Root, [string] $Name) {
    Wait-Until "profile $Name" { $null -ne (Find-ByNamePrefix $Root "$Name, " 'ListItem') } 15
    Select-Element (Find-ByNamePrefix $Root "$Name, " 'ListItem')
}

# The menu of the notification-area icon is a window of Windows itself, and its entries are not in the UI Automation tree that
# this script can read, so a choice in it is made with the keyboard. A key goes to whichever window has the keyboard, so none is
# sent unless it is the app's own (the menu counts): UiNative refuses otherwise, and the part of the run that needs it is skipped.
function Open-TrayMenu($App) {
    $line = Get-Content $App.Report -Encoding utf8 | Where-Object { $_ -like 'tray_window=*' } | Select-Object -First 1
    $tray = [IntPtr][int64]($line -replace 'tray_window=', '')
    [UiNative]::SetForegroundWindow($App.Process.MainWindowHandle) | Out-Null
    [UiNative]::PostMessage($tray, $WmApp + 1, [UIntPtr][uint32][UiNative]::MakeLong(3000, 1500), [IntPtr]$WmContextMenu) | Out-Null
    Start-Sleep -Milliseconds 800
}

# Chooses the last entry of the tray menu, Quit, which is the one the Up key reaches from the top.
function Choose-QuitInTrayMenu($App) {
    Open-TrayMenu $App
    [UiNative]::PressVirtualKey($App.Process.Id, $VkUp); Start-Sleep -Milliseconds 300
    [UiNative]::PressVirtualKey($App.Process.Id, $VkReturn)
}

# Chooses Quit and waits for the question that asks whether to disconnect first. False when the app could not take the keyboard.
function Open-QuitQuestion($App, $Root, [string] $CancelLabel) {
    try {
        foreach ($attempt in 1..3) {
            Choose-QuitInTrayMenu $App
            Start-Sleep -Milliseconds 1500
            if ($null -ne (Find-ByName $Root $CancelLabel 'Button')) { return $true }
        }
    }
    catch [System.InvalidOperationException] { Write-Host $_.Exception.Message }
    return $false
}

# Opens the menu and takes a picture of it. The menu is closed by the window message that ends menu mode, not by a key.
function Open-TrayMenuScreenshot($App, [string] $Path) {
    Open-TrayMenu $App
    $captured = [UiNative]::CaptureOpenMenu($Path)
    $line = Get-Content $App.Report -Encoding utf8 | Where-Object { $_ -like 'tray_window=*' } | Select-Object -First 1
    [UiNative]::PostMessage([IntPtr][int64]($line -replace 'tray_window=', ''), $WmCancelMode, [UIntPtr]::Zero, [IntPtr]::Zero) | Out-Null
    Start-Sleep -Milliseconds 400
    return $captured
}
# Presses a button, such as the one of a dialog that closes it.
function Press-DialogButton($Root, [string] $Name) {
    Wait-Until "the '$Name' button" { $null -ne (Find-ByName $Root $Name 'Button') } 10
    Invoke-Element (Find-ByName $Root $Name 'Button')
    Start-Sleep -Milliseconds 600
}

try {
    New-Item -ItemType Directory -Force $ScratchDirectory | Out-Null
    if ($Build) {
        & dotnet build (Join-Path $RepositoryRoot 'windows\Plaitway.sln') -c $Configuration
        if ($LASTEXITCODE -ne 0) { throw 'dotnet build failed' }
    }
    $script:DaemonExe = Join-Path $ScratchDirectory 'plaitwayd.exe'
    $script:CliExe = Join-Path $ScratchDirectory 'plaitway.exe'
    Build-Go './cmd/plaitwayd' $script:DaemonExe
    Build-Go './cmd/plaitway' $script:CliExe

    foreach ($theme in $Themes) {
        foreach ($language in $Languages) {
            $suffix = "$theme-$language"
            $t = { param($key) Get-Text $key $language }
            $daemon = Start-Daemon
            Add-Profiles $daemon.Pipe
            $app = Start-App $theme $language $daemon.Pipe $suffix
            $window = $app.Process.MainWindowHandle
            $root = [System.Windows.Automation.AutomationElement]::FromHandle($window)
            Wait-Until 'the profiles in the sidebar' { $null -ne (Find-ByNamePrefix $root 'Broken, ' 'ListItem') } $StartupSeconds
            Start-Sleep -Milliseconds 1500

            # The five tabs of a connected profile.
            Select-Profile $root 'Office'
            Save-Page 'overview' $app $suffix
            Select-ByName $root (& $t 'Routes and DNS') 'TabItem'; Save-Page 'routes' $app $suffix
            Select-ByName $root (& $t 'Logs') 'TabItem'; Start-Sleep -Milliseconds 1200; Save-Page 'logs' $app $suffix
            Select-ByName $root (& $t 'Configuration') 'TabItem'; Start-Sleep -Milliseconds 1200; Save-Page 'configuration' $app $suffix
            Select-ByName $root (& $t 'Settings') 'TabItem'; Save-Page 'settings' $app $suffix

            # A WireGuard profile, which has a public key and a setting of its own, and one that failed.
            Select-Profile $root 'Home'
            Select-ByName $root (& $t 'Overview') 'TabItem'; Save-Page 'overview-wireguard' $app $suffix
            Select-Profile $root 'Broken'; Save-Page 'overview-failed' $app $suffix

            # Diagnostics, with its second page, and the app's settings.
            Select-ByName $root (& $t 'Diagnostics') 'ListItem'; Start-Sleep -Milliseconds 1500; Save-Page 'diagnostics' $app $suffix
            Select-ByName $root (& $t 'Helper log') 'TabItem'; Start-Sleep -Milliseconds 1200; Save-Page 'helper-log' $app $suffix
            Select-ByName $root (& $t 'Settings') 'ListItem'; Save-Page 'app-settings' $app $suffix

            # The question before a profile is deleted, and the question before the app quits with a profile on.
            Select-Profile $root 'Lab'
            Select-ByName $root (& $t 'Settings') 'TabItem'
            Press-DialogButton $root (& $t 'Delete Profile…'); Start-Sleep -Milliseconds 600
            Save-Page 'dialog-delete' $app $suffix
            Press-DialogButton $root (& $t 'Cancel')

            if ($suffix -eq "$($Themes[0])-$($Languages[0])") {
                $summary.Add('tray menu captured: ' + (Open-TrayMenuScreenshot $app (Join-Path $ArtifactsDirectory "tray-menu-$suffix.png")))
            }
            if (Open-QuitQuestion $app $root (& $t 'Cancel')) {
                Save-Page 'dialog-quit' $app $suffix
                Press-DialogButton $root (& $t 'Cancel')
                $summary.Add("the app stays after cancelling Quit: $(-not $app.Process.HasExited)")
            }
            else {
                $summary.Add("the question before quitting was skipped in ${suffix}: the app could not take the keyboard")
            }
            Stop-Process -Id $daemon.Process.Id -Force
        }
    }

    # The window with no helper to talk to, and the dialog of a profile that asks for a password.
    $suffix = "$($Themes[0])-$($Languages[0])"
    $app = Start-App $Themes[0] $Languages[0] '\\.\pipe\plaitway-ui-nobody-here' 'setup'
    Start-Sleep -Milliseconds 2500
    Save-Page 'setup' $app $suffix
    Stop-Process -Id $app.Process.Id -Force

    $daemon = Start-Daemon
    Add-Profiles $daemon.Pipe -WithCredentials
    $app = Start-App $Themes[0] $Languages[0] $daemon.Pipe 'credentials'
    $root = [System.Windows.Automation.AutomationElement]::FromHandle($app.Process.MainWindowHandle)
    Wait-Until 'the profiles in the sidebar' { $null -ne (Find-ByNamePrefix $root 'Router, ' 'ListItem') } $StartupSeconds
    Select-Profile $root 'Router'
    Start-Sleep -Milliseconds 800
    Invoke-Element (Find-ByName $root (Get-Text 'Connect' $Languages[0]) 'Button')
    Start-Sleep -Milliseconds 1500
    Save-Page 'dialog-credentials' $app $suffix
    Press-DialogButton $root (Get-Text 'Cancel' $Languages[0])
    Stop-Process -Id $app.Process.Id -Force
}
finally {
    foreach ($process in $processes) {
        if ($process -and -not $process.HasExited) { Stop-Process -Id $process.Id -Force }
    }
    if (Test-Path $ScratchDirectory) { Remove-Item $ScratchDirectory -Recurse -Force -ErrorAction SilentlyContinue }
    [IO.File]::WriteAllLines((Join-Path $ArtifactsDirectory 'summary.txt'), $summary, (New-Object Text.UTF8Encoding $false))
    $summary | ForEach-Object { Write-Host $_ }
}
if ($failures.Count -gt 0) { Write-Host "`n$($failures.Count) problems"; exit 1 }
