<#
.SYNOPSIS
Builds the Plaitway MSI: stages the payload, then builds one package per language.

.DESCRIPTION
1. Builds plaitwayd.exe and plaitway.exe with the version of the VERSION file.
2. Fetches the checked wintun.dll (fetch-wintun.ps1: pinned hashes and signature).
3. Publishes the app self-contained, so the machine needs no Windows App Runtime.
4. Writes the notices and the licence of the package.
5. Signs the programs of Plaitway and the packages when -CertificateThumbprint is given.
6. Builds windows\installer for each language. The ProductCode is derived from the
   upgrade code, the version, the language and the files of the payload, so a build
   with other files is an upgrade of the earlier one and the same files give the
   same code.

Nothing is installed and nothing outside the repository's build folder is written.

.PARAMETER Culture
en-US, zh-TW or All. Default: All.

.PARAMETER OutputDirectory
Where the stage and the finished packages go. Default: build\windows below the
repository, which git ignores.

.PARAMETER CertificateThumbprint
A code signing certificate of the current user or the machine, with its private key. The programs of Plaitway (plaitwayd,
plaitway and the app's own assemblies) are signed before the packages are built, and the packages after. wintun.dll and
the libraries of others keep their own signatures. Without it the output is unsigned.

.PARAMETER TimestampServer
The RFC 3161 server for the signatures. Default: the one in lib\Sign.ps1.

.PARAMETER AllowUntrustedRoot
Accepts a certificate that this machine does not trust (a self-signed one), to try the script out.

.PARAMETER SkipAppPublish
Reuses the app in <OutputDirectory>\payload\app of an earlier run. For trying out
a change of the installer itself.

.EXAMPLE
.\build-installer.ps1
#>
[CmdletBinding()]
param(
    [ValidateSet('en-US', 'zh-TW', 'All')]
    [string] $Culture = 'All',

    [string] $OutputDirectory,

    [switch] $SkipAppPublish,

    [string] $CertificateThumbprint,

    [string] $TimestampServer,

    [switch] $AllowUntrustedRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'lib\Msi.ps1')
. (Join-Path $PSScriptRoot 'lib\Sign.ps1')

$RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$AllCultures = @('en-US', 'zh-TW')
$WindowsArchitecture = 'amd64'
$GoMachine = 'amd64'
$InstallerPlatform = 'x64'
$UpgradeCode = 'BDA29F25-493D-4DF8-8B16-5A612BC4E954'
$VersionPattern = '^\d+\.\d+\.\d+$'
$MaxMsiMajor = 255
$MaxMsiMinor = 255
$MaxMsiBuild = 65535
$ProductCodeNamespace = 'plaitway-msi-product-code'
$PayloadExecutables = @('plaitwayd.exe', 'plaitway.exe', 'wintun.dll', 'app\Plaitway.exe')
# What Plaitway itself builds and therefore signs, relative to the payload; the files of others keep their signatures.
$OwnPayloadPatterns = @('plaitwayd.exe', 'plaitway.exe', 'app\Plaitway.exe', 'app\Plaitway.dll', 'app\Plaitway.*.dll')
$SigningOptions = @{}

if (-not $OutputDirectory) { $OutputDirectory = Join-Path $RepositoryRoot 'build\windows' }
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
$PayloadDirectory = Join-Path $OutputDirectory 'payload'

function Write-Step([string] $Message) {
    Write-Host "==> $Message" -ForegroundColor Cyan
}

# A native command that fails must stop the script: $ErrorActionPreference does not see exit codes.
function Invoke-Native([string] $Program, [string[]] $Arguments, [string] $WorkingDirectory = $RepositoryRoot) {
    Push-Location $WorkingDirectory
    try {
        # The output goes to the console, not into the value of the function that called this one.
        & $Program @Arguments | Out-Host
        if ($LASTEXITCODE -ne 0) { throw "$Program $($Arguments -join ' ') exited with $LASTEXITCODE." }
    }
    finally { Pop-Location }
}

function Get-ReleaseVersion {
    $version = (Get-Content -LiteralPath (Join-Path $RepositoryRoot 'VERSION') -Raw).Trim()
    if ($version -notmatch $VersionPattern) { throw "VERSION is '$version'; the package needs major.minor.patch." }
    $parts = $version.Split('.') | ForEach-Object { [int] $_ }
    if ($parts[0] -gt $MaxMsiMajor -or $parts[1] -gt $MaxMsiMinor -or $parts[2] -gt $MaxMsiBuild) {
        throw "Version $version does not fit a Windows Installer version (at most $MaxMsiMajor.$MaxMsiMinor.$MaxMsiBuild)."
    }
    return $version
}

# Only what is below the build folder may be emptied, whatever -OutputDirectory says.
function Reset-Directory([string] $Path) {
    $full = [IO.Path]::GetFullPath($Path)
    if (-not $full.StartsWith($OutputDirectory + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to empty ${full}: it is not below ${OutputDirectory}."
    }
    if (Test-Path -LiteralPath $full) { [IO.Directory]::Delete($full, $true) }
    [void] (New-Item -ItemType Directory -Path $full)
}

function Get-OwnPayloadFiles {
    foreach ($pattern in $OwnPayloadPatterns) {
        Get-ChildItem -Path (Join-Path $PayloadDirectory $pattern) -File | ForEach-Object FullName
    }
}

function Build-GoProgram([string] $Package, [string] $Output, [string] $Version) {
    Write-Step "go build $Package"
    $environment = @{ GOOS = 'windows'; GOARCH = $GoMachine; CGO_ENABLED = '0' }
    $previous = @{}
    foreach ($name in $environment.Keys) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $environment[$name], 'Process')
    }
    try {
        Invoke-Native 'go' @('build', '-trimpath', '-buildvcs=false', '-ldflags', "-s -w -X main.version=$Version", '-o', $Output, $Package)
    }
    finally {
        foreach ($name in $environment.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    }
}

function Publish-App([string] $Version, [string] $Destination) {
    Write-Step 'dotnet publish the app (self-contained)'
    $project = Join-Path $RepositoryRoot 'windows\Sources\Plaitway.App\Plaitway.App.csproj'
    Invoke-Native 'dotnet' @(
        'publish', $project, '-c', 'Release', '-p:PlaitwaySelfContained=true', "-p:Version=$Version",
        '-o', $Destination)
}

# The licence of the package as RTF for the setup dialog: the text of LICENSE, escaped.
function ConvertTo-Rtf([string] $Text) {
    $builder = New-Object System.Text.StringBuilder
    [void] $builder.Append('{\rtf1\ansi\deff0{\fonttbl{\f0 Segoe UI;}}\f0\fs20 ')
    foreach ($character in $Text.ToCharArray()) {
        switch -Regex ($character) {
            '[\\{}]' { [void] $builder.Append('\').Append($character) }
            "`n" { [void] $builder.Append('\par ') }
            "`r" { }
            default {
                $code = [int] $character
                if ($code -gt 127) {
                    # RTF writes a code point as a signed 16-bit number, followed by a replacement for readers that skip it.
                    if ($code -gt [int16]::MaxValue) { $code -= 65536 }
                    [void] $builder.Append('\u').Append($code).Append('?')
                }
                else { [void] $builder.Append($character) }
            }
        }
    }
    [void] $builder.Append('}')
    return $builder.ToString()
}

function Write-Licenses([string] $Destination, [string] $PublishedApp) {
    Write-Step 'notices and licence'
    $licenses = Join-Path $Destination 'licenses'
    [void] (New-Item -ItemType Directory -Path $licenses)
    $licenseText = Get-Content -LiteralPath (Join-Path $RepositoryRoot 'LICENSE') -Raw
    [IO.File]::WriteAllText((Join-Path $licenses 'LICENSE.txt'), $licenseText, (New-Object System.Text.UTF8Encoding $false))
    [IO.File]::WriteAllText((Join-Path $Destination 'License.rtf'), (ConvertTo-Rtf $licenseText), [Text.Encoding]::ASCII)
    Invoke-Native 'go' @('run', './packaging/notices', '-windows', '-publish', $PublishedApp, '-goarch', $GoMachine, '-o', (Join-Path $licenses 'THIRD_PARTY_NOTICES.md'))
}

function Get-FileHashLines([string] $Directory, [string[]] $Include) {
    $files = Get-ChildItem -LiteralPath $Directory -Recurse -File -Include $Include |
        Where-Object { $_.FullName -notmatch '\\(obj|bin)\\' } | Sort-Object { $_.FullName.ToLowerInvariant() }
    foreach ($file in $files) {
        $relative = $file.FullName.Substring($Directory.Length).TrimStart('\').ToLowerInvariant()
        '{0} {1}' -f $relative, (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash
    }
}

# One hash over the relative names and contents of the payload and of the sources of the package, in a fixed order: a
# change of either is another ProductCode, so that the new package replaces the installed one as an upgrade.
function Get-PayloadFingerprint([string] $Directory) {
    $installerSources = Join-Path $RepositoryRoot 'windows\installer'
    $lines = @(Get-FileHashLines $Directory @('*')) + @(Get-FileHashLines $installerSources @('*.wxs', '*.wxl', '*.wixproj'))
    $bytes = [Text.Encoding]::UTF8.GetBytes(($lines -join "`n"))
    return [BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($bytes)).Replace('-', '')
}

# A name-based GUID: the same inputs give the same ProductCode, other files give another.
function Get-ProductCode([string] $Version, [string] $CultureName, [string] $Fingerprint) {
    $seed = '{0}|{1}|{2}|{3}|{4}|{5}' -f $ProductCodeNamespace, $UpgradeCode, $Version, $InstallerPlatform, $CultureName, $Fingerprint
    $hash = [Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($seed))
    $guidBytes = New-Object byte[] 16
    [Array]::Copy($hash, $guidBytes, 16)
    $guidBytes[7] = ($guidBytes[7] -band 0x0F) -bor 0x50   # version 5 style, as a name-based id
    $guidBytes[8] = ($guidBytes[8] -band 0x3F) -bor 0x80
    return ([Guid] $guidBytes).ToString('B').ToUpperInvariant()
}

function Build-Package([string] $Version, [string] $CultureName, [string] $Fingerprint) {
    Write-Step "msi $CultureName"
    $productCode = Get-ProductCode $Version $CultureName $Fingerprint
    $project = Join-Path $RepositoryRoot 'windows\installer\Plaitway.Installer.wixproj'
    Invoke-Native 'dotnet' @(
        'build', $project, '-c', 'Release', '-nologo',
        "-p:InstallerPlatform=$InstallerPlatform", "-p:ProductVersion=$Version", "-p:ProductCode=$productCode",
        "-p:PayloadDir=$PayloadDirectory", "-p:Cultures=$CultureName")
    $built = Join-Path $RepositoryRoot "windows\installer\bin\$InstallerPlatform\Release\$CultureName\Plaitway.msi"
    $target = Join-Path $OutputDirectory ("Plaitway-{0}-{1}-{2}.msi" -f $Version, $InstallerPlatform, $CultureName)
    Copy-Item -LiteralPath $built -Destination $target -Force
    if ($script:SigningCertificate) { Add-Signature -Path $target -Certificate $script:SigningCertificate @SigningOptions }
    return [pscustomobject]@{ Path = $target; ProductCode = $productCode }
}

# Reads the finished package back, so that a wrong property is found here and not on the machine of a user.
function Assert-Package($Package, [string] $Version) {
    $handle = Open-MsiDatabase $Package.Path
    try {
        $properties = Get-MsiProperties $handle
        $expected = @{ ProductVersion = $Version; ProductCode = $Package.ProductCode; UpgradeCode = "{$UpgradeCode}" }
        foreach ($name in $expected.Keys) {
            if ($properties[$name] -ne $expected[$name]) { throw "$($Package.Path): $name is '$($properties[$name])', expected '$($expected[$name])'." }
        }
        $fileNames = (Invoke-MsiQuery $handle 'SELECT `FileName` FROM `File`') | ForEach-Object { ($_[0] -split '\|')[-1] }
        foreach ($executable in $PayloadExecutables) {
            $leaf = Split-Path $executable -Leaf
            if ($fileNames -notcontains $leaf) { throw "$($Package.Path) holds no $leaf." }
        }
    }
    finally { Close-MsiDatabase $handle }
}

$version = Get-ReleaseVersion
$script:SigningCertificate = $null
if ($CertificateThumbprint) {
    $script:SigningCertificate = Get-SigningCertificate $CertificateThumbprint
    $SigningOptions['AllowUntrustedRoot'] = [bool] $AllowUntrustedRoot
    if ($TimestampServer) { $SigningOptions['TimestampServer'] = $TimestampServer }
}
$cultures = if ($Culture -eq 'All') { $AllCultures } else { @($Culture) }

[void] (New-Item -ItemType Directory -Force -Path $OutputDirectory)
if ($SkipAppPublish) {
    if (-not (Test-Path -LiteralPath (Join-Path $PayloadDirectory 'app\Plaitway.exe'))) { throw "-SkipAppPublish needs the app of an earlier run in $PayloadDirectory\app." }
    $keptApp = Join-Path $OutputDirectory 'kept-app'
    Reset-Directory $keptApp
    Move-Item -LiteralPath (Join-Path $PayloadDirectory 'app') -Destination (Join-Path $keptApp 'app')
}
Reset-Directory $PayloadDirectory

Build-GoProgram './cmd/plaitwayd' (Join-Path $PayloadDirectory 'plaitwayd.exe') $version
Build-GoProgram './cmd/plaitway' (Join-Path $PayloadDirectory 'plaitway.exe') $version

Write-Step 'wintun.dll'
& (Join-Path $PSScriptRoot 'fetch-wintun.ps1') -OutputDirectory $PayloadDirectory -Architecture $WindowsArchitecture

$appDirectory = Join-Path $PayloadDirectory 'app'
if ($SkipAppPublish) { Move-Item -LiteralPath (Join-Path $OutputDirectory 'kept-app\app') -Destination $appDirectory }
else { Publish-App $version $appDirectory }

Write-Licenses $PayloadDirectory $appDirectory
if ($script:SigningCertificate) {
    Write-Step 'sign the programs of Plaitway'
    Add-Signature -Path @(Get-OwnPayloadFiles) -Certificate $script:SigningCertificate @SigningOptions
}
$fingerprint = Get-PayloadFingerprint $PayloadDirectory

$packages = foreach ($cultureName in $cultures) { Build-Package $version $cultureName $fingerprint }
foreach ($package in $packages) { Assert-Package $package $version }

Write-Step 'done'
$packages | ForEach-Object { Write-Host ("{0}  {1}" -f $_.ProductCode, $_.Path) }
