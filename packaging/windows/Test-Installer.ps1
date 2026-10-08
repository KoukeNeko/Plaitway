<#
.SYNOPSIS
Checks a finished Plaitway MSI without installing it.

.DESCRIPTION
Reads the package as a database and unpacks it with an administrative install
(msiexec /a), which copies the files to a folder and touches nothing else: no
service, no registry key, no shortcut. It checks

  - the properties: version, upgrade code, per machine, the language of the culture;
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
$LanguageIds = @{ 'en-US' = '1033'; 'zh-TW' = '1028' }
# Relative to the product folder (the one that holds plaitwayd.exe), where the app finds the daemon one folder above its own.
$RequiredFiles = @(
    'plaitwayd.exe', 'plaitway.exe', 'wintun.dll', 'app\Plaitway.exe',
    # The app's compiled XAML and strings: a publish without them dies at the first resource lookup.
    'app\Plaitway.pri',
    'licenses\LICENSE.txt', 'licenses\THIRD_PARTY_NOTICES.md')
$WintunSigner = 'WireGuard LLC'
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

function Get-CultureOfPackage([string] $Path) {
    foreach ($name in $LanguageIds.Keys) { if ($Path -like "*-$name.msi") { return $name } }
    throw "Cannot tell the language of $Path from its name (Plaitway-<version>-<platform>-<culture>.msi)."
}

# The Sequence column of an action in InstallExecuteSequence, which WiX fills in when it orders the actions.
function Get-ActionSequence($Handle) {
    $sequence = @{}
    foreach ($row in (Invoke-MsiQuery $Handle 'SELECT `Action`, `Sequence` FROM `InstallExecuteSequence`')) {
        if ($row[1] -ne '') { $sequence[$row[0]] = [int] $row[1] }
    }
    return $sequence
}

function Test-Database($Handle, [string] $Culture) {
    $properties = Get-MsiProperties $Handle
    Assert-That ($properties['UpgradeCode'] -eq $UpgradeCode) 'the upgrade code is the one of the product'
    Assert-That ($properties['ProductLanguage'] -eq $LanguageIds[$Culture]) "the language is $Culture ($($LanguageIds[$Culture]))"
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

    # Found by installing: an upgrade with a language only replaces a product of that language, and a quote written as an entity
    # reaches WixQuietExec as the text &quot;.
    $upgradeRows = Invoke-MsiQuery $Handle 'SELECT `Language` FROM `Upgrade`'
    Assert-That (@($upgradeRows | Where-Object { $_[0] -ne '' }).Count -eq 0) 'the upgrade replaces a product in any language'
    $commandLines = @($customActions | Where-Object { $_[0] -like 'SetHelper*Data' })
    $targets = Invoke-MsiQuery $Handle 'SELECT `Action`, `Target` FROM `CustomAction`'
    $badCommandLines = @($targets | Where-Object { $_[0] -like 'SetHelper*Data' -and ($_[1] -notmatch '^"' -or $_[1] -match '&quot;') })
    Assert-That ($commandLines.Count -eq 7 -and $badCommandLines.Count -eq 0) 'the seven command lines of the Helper actions begin with a quote and hold no entity'

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
    $culture = Get-CultureOfPackage $path
    Write-Host "== $path ($culture)" -ForegroundColor Cyan
    $handle = Open-MsiDatabase $path
    try { Test-Database $handle $culture } finally { Close-MsiDatabase $handle }

    $target = Join-Path $ScratchDirectory $culture
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
