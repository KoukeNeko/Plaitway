<#
.SYNOPSIS
Checks a finished Plaitway MSI without installing it.

.DESCRIPTION
Reads the package as a database and unpacks it with an administrative install
(msiexec /a), which copies the files to a folder and touches nothing else: no
service, no registry key, no shortcut. It checks

  - the properties: version, upgrade code, per machine, the language of the setup;
  - that the files, the custom actions and their order are those the design says
    (the Helper actions around InstallFiles and RemoveFiles, and no ServiceInstall
    rows, because the daemon registers its own service);
  - that the unpacked folder holds the programs, a wintun.dll signed by WireGuard
    LLC, the licences and the app with its resources.

Installing, upgrading and removing the real service is not done here: see
windows\installer\INSTALL-TEST.md, which is run in a virtual machine.

.PARAMETER Package
The .msi to check. Default: every Plaitway-*.msi in build\windows.

.PARAMETER ScratchDirectory
Where the package is unpacked. Default: a new folder below %TEMP%; it is removed afterwards.
#>
[CmdletBinding()]
param(
    [string[]] $Package,

    [string] $ScratchDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\Msi.ps1')

$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$UpgradeCode = '{BDA29F25-493D-4DF8-8B16-5A612BC4E954}'
# The setup dialog is in English whoever installs: the app has its own language.
$SetupLanguageId = '1033'
# Relative to the product folder (the one that holds plaitwayd.exe), where the app finds the daemon one folder above its own.
$RequiredFiles = @(
    'plaitwayd.exe', 'plaitway.exe', 'wintun.dll', 'app\Plaitway.exe',
    'openvpn\bin\openvpn.exe', 'openvpn\bin\tapctl.exe', 'openvpn\bin\libcrypto-3-x64.dll', 'openvpn\bin\libssl-3-x64.dll',
    'openvpn\bin\libpkcs11-helper-1.dll', 'openvpn\bin\vcruntime140.dll', 'openvpn\ssl\modules\legacy.dll', 'openvpn\license.txt',
    'openvpn\driver\OemVista.inf', 'openvpn\driver\tap0901.sys', 'openvpn\driver\tap0901.cat',
    'licenses\LICENSE.txt', 'licenses\THIRD_PARTY_NOTICES.md', 'licenses\THIRD_PARTY_NOTICES_RUST.md')
$WintunSigner = 'WireGuard LLC'
$OpenVpnSigner = 'OpenVPN Inc.'
$TapDriverPublisher = 'Microsoft Windows Hardware Compatibility Publisher'
$HelperActionOrder = @(
    # Each pair is (earlier, later) in InstallExecuteSequence.
    @('HelperRestore', 'HelperStop'),
    @('HelperStop', 'RemoveFiles'),
    @('InstallFiles', 'HelperUndo'),
    @('HelperUndo', 'HelperInstall'),
    @('HelperUndoRemove', 'HelperUninstall'),
    @('HelperUninstall', 'RemoveFiles'),
    @('HelperUninstallForced', 'RemoveFiles')
)

$script:Failures = New-Object System.Collections.Generic.List[string]

function Assert-That([bool] $Condition, [string] $Description) {
    if ($Condition) { Write-Host "  ok    $Description" -ForegroundColor Green }
    else { Write-Host "  FAIL  $Description" -ForegroundColor Red; $script:Failures.Add($Description) }
}

# The Sequence column of an action in InstallExecuteSequence, which WiX fills in when it orders the actions.
function Get-ActionSequence($Handle) {
    $sequence = @{}
    foreach ($row in (Invoke-MsiQuery $Handle 'SELECT `Action`, `Sequence` FROM `InstallExecuteSequence`')) {
        if ($row[1] -ne '') { $sequence[$row[0]] = [int] $row[1] }
    }
    return $sequence
}

function Test-Database($Handle) {
    $properties = Get-MsiProperties $Handle
    Assert-That ($properties['UpgradeCode'] -eq $UpgradeCode) 'the upgrade code is the one of the product'
    Assert-That ($properties['ProductLanguage'] -eq $SetupLanguageId) "the language of the setup is English ($SetupLanguageId)"
    Assert-That ($properties['ProductVersion'] -match '^\d+\.\d+\.\d+$') "the version is major.minor.patch ($($properties['ProductVersion']))"
    Assert-That ($properties['ProductVersion'] -eq (Get-Content (Join-Path $RepositoryRoot 'VERSION')).Trim()) 'the version is the one of the VERSION file'
    Assert-That (-not (Test-MsiTable $Handle 'ServiceInstall')) 'there are no ServiceInstall rows: the daemon registers its own service'
    Assert-That (-not (Test-MsiTable $Handle 'ServiceControl')) 'there are no ServiceControl rows'

    $summaryAllUsers = (Get-MsiSummaryProperty $Handle 15) -band 8
    Assert-That ($summaryAllUsers -eq 0) 'the package is not marked for a per-user install'
    $allUsers = (Invoke-MsiQuery $Handle 'SELECT `Value` FROM `Property` WHERE `Property` = ''ALLUSERS''')
    Assert-That (@($allUsers).Count -eq 1 -and $allUsers[0][0] -eq '1') 'ALLUSERS is 1: per machine'

    $sequence = Get-ActionSequence $Handle
    foreach ($pair in $HelperActionOrder) {
        $known = $sequence.ContainsKey($pair[0]) -and $sequence.ContainsKey($pair[1])
        Assert-That ($known -and $sequence[$pair[0]] -lt $sequence[$pair[1]]) "$($pair[0]) runs before $($pair[1])"
    }
    # Windows Installer SQL has no LIKE: the filter is ours.
    $customActions = Invoke-MsiQuery $Handle 'SELECT `Action`, `Type` FROM `CustomAction`'
    $actions = @($customActions | Where-Object { $_[0] -like 'Helper*' })
    $deferredTypeBit = 1024
    $inScript = @($actions | Where-Object { ([int] $_[1] -band $deferredTypeBit) -ne 0 })
    Assert-That (@($actions).Count -eq 7 -and $inScript.Count -eq 7) 'the seven Helper actions are deferred or rollback (they run as SYSTEM)'

    # Found by installing: an upgrade with a language only replaces a product of that language (0.6.0 had a package for each of two
    # languages, and a person who has the other one gets this), and a quote written as an entity reaches WixQuietExec as the text
    # &quot;.
    $upgradeRows = Invoke-MsiQuery $Handle 'SELECT `Language` FROM `Upgrade`'
    Assert-That (@($upgradeRows | Where-Object { $_[0] -ne '' }).Count -eq 0) 'the upgrade replaces a product in any language'
    $commandLines = @($customActions | Where-Object { $_[0] -like 'SetHelper*Data' })
    $targets = Invoke-MsiQuery $Handle 'SELECT `Action`, `Target` FROM `CustomAction`'
    $badCommandLines = @($targets | Where-Object { $_[0] -like 'SetHelper*Data' -and ($_[1] -notmatch '^"' -or $_[1] -match '&quot;') })
    Assert-That ($commandLines.Count -eq 7 -and $badCommandLines.Count -eq 0) 'the seven command lines of the Helper actions begin with a quote and hold no entity'

    # The driver goes into the driver store by pnputil, as SYSTEM, and nothing removes it again (it is shared with an OpenVPN of the person's).
    $stage = @($targets | Where-Object { $_[0] -eq 'SetTapDriverStageData' })
    Assert-That ($stage.Count -eq 1 -and $stage[0][1] -match '^"\[System64Folder\]pnputil\.exe" /add-driver "\[OPENVPNFOLDER\]driver\\OemVista\.inf"$') 'the driver is added with pnputil /add-driver from openvpn\driver\OemVista.inf'
    $stageAction = @($customActions | Where-Object { $_[0] -eq 'TapDriverStage' })
    Assert-That ($stageAction.Count -eq 1 -and ([int] $stageAction[0][1] -band 1024) -ne 0) 'the driver is added by a deferred action (it runs as SYSTEM)'
    Assert-That ($sequence.ContainsKey('TapDriverStage') -and $sequence.ContainsKey('InstallFiles') -and $sequence['InstallFiles'] -lt $sequence['TapDriverStage']) 'the driver is added after the files are in'

    $shortcuts = Invoke-MsiQuery $Handle 'SELECT `Name` FROM `Shortcut`'
    Assert-That (@($shortcuts).Count -eq 1) 'one shortcut: the Start menu entry'
}

function Test-UnpackedFolder([string] $Folder) {
    $daemon = Get-ChildItem -LiteralPath $Folder -Recurse -File -Filter 'plaitwayd.exe' | Select-Object -First 1
    Assert-That ($null -ne $daemon) 'the package holds plaitwayd.exe'
    if ($null -eq $daemon) { return }
    $product = $daemon.DirectoryName
    foreach ($relative in $RequiredFiles) {
        Assert-That (Test-Path -LiteralPath (Join-Path $product $relative) -PathType Leaf) "the package holds $relative"
    }
    foreach ($name in 'openvpn.exe', 'tapctl.exe') {
        $path = Join-Path $product "openvpn\bin\$name"
        if (Test-Path -LiteralPath $path) {
            $signature = Get-AuthenticodeSignature -LiteralPath $path
            Assert-That ($signature.Status -eq 'Valid' -and $signature.SignerCertificate.Subject -like "*$OpenVpnSigner*") "$name is signed by $OpenVpnSigner"
        }
    }
    $driver = Join-Path $product 'openvpn\driver\tap0901.sys'
    if (Test-Path -LiteralPath $driver) {
        $signature = Get-AuthenticodeSignature -LiteralPath $driver
        Assert-That ($signature.Status -eq 'Valid' -and $signature.SignerCertificate.Subject -like "*$TapDriverPublisher*") "tap0901.sys is signed by $TapDriverPublisher"
    }
    $wintun = Join-Path $product 'wintun.dll'
    if (Test-Path -LiteralPath $wintun) {
        $signature = Get-AuthenticodeSignature -LiteralPath $wintun
        Assert-That ($signature.Status -eq 'Valid' -and $signature.SignerCertificate.Subject -like "*$WintunSigner*") "wintun.dll is signed by $WintunSigner"
    }
}

function Expand-Package([string] $Path, [string] $Destination) {
    $arguments = @('/a', $Path, '/qn', "TARGETDIR=$Destination")
    $process = Start-Process -FilePath msiexec.exe -ArgumentList $arguments -Wait -PassThru -WindowStyle Hidden
    if ($process.ExitCode -ne 0) { throw "msiexec /a exited with $($process.ExitCode) for $Path." }
}

if (-not $Package) { $Package = Get-ChildItem -Path (Join-Path $RepositoryRoot 'build\windows') -Filter 'Plaitway-*.msi' | ForEach-Object FullName }
if (-not $Package) { throw 'No package to check: run packaging\windows\build-installer.ps1 first, or give -Package.' }
if (-not $ScratchDirectory) { $ScratchDirectory = Join-Path ([IO.Path]::GetTempPath()) ('plaitway-installer-check-' + [Guid]::NewGuid().ToString('N')) }

foreach ($path in $Package) {
    Write-Host "== $path" -ForegroundColor Cyan
    $handle = Open-MsiDatabase $path
    try { Test-Database $handle } finally { Close-MsiDatabase $handle }

    $target = Join-Path $ScratchDirectory ([IO.Path]::GetFileNameWithoutExtension($path))
    [void] (New-Item -ItemType Directory -Path $target -Force)
    try {
        Expand-Package $path $target
        Test-UnpackedFolder $target
    }
    finally { if (Test-Path -LiteralPath $ScratchDirectory) { [IO.Directory]::Delete($ScratchDirectory, $true) } }
}

if ($script:Failures.Count -gt 0) {
    Write-Host "$($script:Failures.Count) check(s) failed." -ForegroundColor Red
    exit 1
}
Write-Host 'All checks passed.' -ForegroundColor Green
