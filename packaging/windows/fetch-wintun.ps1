<#
.SYNOPSIS
Fetches wintun.dll for one architecture and puts it in a folder, after checking it.

.DESCRIPTION
Downloads the official wintun archive into a new, empty working folder and
refuses to go on unless every check passes:

  1. the SHA-256 of the archive is the one pinned in wintun\wintun.json;
  2. the SHA-256 of the DLL taken out of it is the one pinned for its
     architecture;
  3. the DLL has a valid Authenticode signature, issued to the signer named in
     wintun.json (WireGuard LLC).

Only then is the DLL copied, as wintun.dll, into OutputDirectory. The DLL is not
committed to the repository; the installer and the build of the daemon call this
script. Nothing is installed: the driver inside the DLL is installed by the
daemon, when it creates its first adapter.

.PARAMETER OutputDirectory
The folder that receives wintun.dll, normally the one that holds plaitwayd.exe.
It is created when it does not exist.

.PARAMETER Architecture
amd64, arm64 or x86. Default: the architecture of this machine.

.PARAMETER WorkDirectory
A folder that does not exist yet, or is empty, for the download. Default: a new
folder below %TEMP%. It is removed afterwards unless -KeepWorkDirectory is given.

.PARAMETER KeepWorkDirectory
Leaves the download and the extracted DLL in the working folder.

.EXAMPLE
.\fetch-wintun.ps1 -OutputDirectory ..\..\bin
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $OutputDirectory,

    [ValidateSet('amd64', 'arm64', 'x86')]
    [string] $Architecture,

    [string] $WorkDirectory,

    [switch] $KeepWorkDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$PinFile = Join-Path $PSScriptRoot 'wintun\wintun.json'
$OutputFileName = 'wintun.dll'
$ArchiveFileName = 'wintun.zip'
$ExtractedFileName = 'wintun.extracted.dll'

function Get-HostArchitecture {
    switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64' { return 'amd64' }
        'Arm64' { return 'arm64' }
        'X86' { return 'x86' }
        default { throw "No wintun.dll exists for the architecture $_." }
    }
}

function Get-FileSha256 {
    param([string] $Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Assert-Sha256 {
    param([string] $Path, [string] $Expected, [string] $What)
    $actual = Get-FileSha256 -Path $Path
    if ($actual -ne $Expected.ToLowerInvariant()) {
        throw "$What has SHA-256 $actual, but $Expected is pinned in $PinFile."
    }
}

function New-EmptyDirectory {
    param([string] $Path)
    if (Test-Path -LiteralPath $Path) {
        if (@(Get-ChildItem -LiteralPath $Path -Force).Count -ne 0) {
            throw "The working folder $Path is not empty."
        }
        return (Resolve-Path -LiteralPath $Path).Path
    }
    return (New-Item -ItemType Directory -Path $Path).FullName
}

# Writes the entry to a file of our own choosing: nothing in the archive decides
# where anything goes.
function Export-ArchiveEntry {
    param([string] $ArchivePath, [string] $EntryName, [string] $Destination)
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($ArchivePath)
    try {
        $entry = $archive.GetEntry($EntryName)
        if ($null -eq $entry) {
            throw "The archive has no entry $EntryName."
        }
        [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $Destination, $false)
    }
    finally {
        $archive.Dispose()
    }
}

function Assert-Signature {
    param([string] $Path, [string] $Signer)
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne 'Valid') {
        throw "The signature of the DLL is $($signature.Status): $($signature.StatusMessage)"
    }
    $name = $signature.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
    if ($name -ne $Signer) {
        throw "The DLL is signed by '$name', not by '$Signer'."
    }
}

function Save-Archive {
    param([string] $Url, [string] $Destination)
    # Windows PowerShell 5.1 does not offer TLS 1.2 by default.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing
}

$pin = Get-Content -LiteralPath $PinFile -Raw | ConvertFrom-Json
if (-not $Architecture) {
    $Architecture = Get-HostArchitecture
}
$dllPin = $pin.dll.$Architecture
if ($null -eq $dllPin) {
    throw "$PinFile pins no DLL for $Architecture."
}

if (-not $WorkDirectory) {
    $WorkDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("plaitway-wintun-" + [guid]::NewGuid().ToString('N'))
}
$work = New-EmptyDirectory -Path $WorkDirectory

try {
    $archivePath = Join-Path $work $ArchiveFileName
    $extractedPath = Join-Path $work $ExtractedFileName

    Save-Archive -Url $pin.url -Destination $archivePath
    Assert-Sha256 -Path $archivePath -Expected $pin.zipSha256 -What 'The downloaded archive'

    Export-ArchiveEntry -ArchivePath $archivePath -EntryName $dllPin.pathInZip -Destination $extractedPath
    Assert-Sha256 -Path $extractedPath -Expected $dllPin.sha256 -What "The $Architecture DLL"
    Assert-Signature -Path $extractedPath -Signer $pin.signer

    $output = New-Item -ItemType Directory -Force -Path $OutputDirectory
    $outputPath = Join-Path $output.FullName $OutputFileName
    Copy-Item -LiteralPath $extractedPath -Destination $outputPath -Force
    Assert-Sha256 -Path $outputPath -Expected $dllPin.sha256 -What 'The copied DLL'

    Write-Host "wintun $($pin.version) ($Architecture), signed by $($pin.signer): $outputPath"
}
finally {
    if (-not $KeepWorkDirectory) {
        Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}
