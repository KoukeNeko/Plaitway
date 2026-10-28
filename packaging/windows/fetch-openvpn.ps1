<#
.SYNOPSIS
Fetches the official OpenVPN for Windows and lays out the part of it that Plaitway ships, after checking it.

.DESCRIPTION
Downloads the OpenVPN installer (an MSI) into a new, empty working folder and refuses to go on unless every check passes:

  1. the SHA-256 of the installer is the one pinned in openvpn\openvpn.json, and its Authenticode signature is valid and
     issued to OpenVPN Inc.;
  2. the programs and libraries taken out of it have the pinned SHA-256, and each of them is signed (OpenVPN Inc.,
     Microsoft for the C runtime);
  3. the TAP-Windows6 driver, which the installer carries inside the library of its custom action, has the pinned
     SHA-256, and tap0901.sys is signed by the Microsoft Windows Hardware Compatibility Publisher (WHQL) and has its
     catalog beside it.

The installer is never run: its files are unpacked with an administrative install (msiexec /a), which writes files and
changes nothing else. OutputDirectory receives:

  bin\       openvpn.exe, tapctl.exe and the libraries openvpn.exe loads
  ssl\modules\legacy.dll
  driver\    OemVista.inf, tap0901.sys, tap0901.cat (staged by the installer of Plaitway with pnputil)
  license.txt

Only x64 exists: the TAP-Windows6 driver package has no arm64 section.

.PARAMETER OutputDirectory
The folder that receives the files, normally <payload>\openvpn. It is created when it does not exist and must be empty.

.PARAMETER WorkDirectory
A folder that does not exist yet, or is empty, for the download. Default: a new folder below %TEMP%. It is removed
afterwards unless -KeepWorkDirectory is given.

.PARAMETER KeepWorkDirectory
Leaves the download and the unpacked installer in the working folder.

.EXAMPLE
.\fetch-openvpn.ps1 -OutputDirectory ..\..\build\windows\payload\openvpn
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $OutputDirectory,

    [string] $WorkDirectory,

    [switch] $KeepWorkDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$PinFile = Join-Path $PSScriptRoot 'openvpn\openvpn.json'
$MicrosoftSigners = @('Microsoft Corporation', 'Microsoft Windows Hardware Compatibility Publisher')

Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;

// Reads a stream of an MSI as it is (the COM ReadStream turns bytes above 0x7F into '?'), and takes a resource out of a
// PE file by walking its resource directory, without loading the file: the library is an installer's custom action, and
// nothing of it should run.
public static class PlaitwayOpenVpnFetch {
    [DllImport("msi", CharSet = CharSet.Unicode)] static extern uint MsiOpenDatabaseW(string path, IntPtr mode, out IntPtr db);
    [DllImport("msi", CharSet = CharSet.Unicode)] static extern uint MsiDatabaseOpenViewW(IntPtr db, string query, out IntPtr view);
    [DllImport("msi")] static extern uint MsiViewExecute(IntPtr view, IntPtr record);
    [DllImport("msi")] static extern uint MsiViewFetch(IntPtr view, out IntPtr record);
    [DllImport("msi")] static extern uint MsiRecordReadStream(IntPtr record, uint field, byte[] buffer, ref uint count);
    [DllImport("msi")] static extern uint MsiCloseHandle(IntPtr handle);

    public static void ExportStream(string msi, string stream, string destination) {
        IntPtr db, view, record;
        if (MsiOpenDatabaseW(msi, IntPtr.Zero, out db) != 0) throw new Exception("Cannot open " + msi);
        try {
            if (MsiDatabaseOpenViewW(db, "SELECT `Name`,`Data` FROM `_Streams` WHERE `Name`='" + stream + "'", out view) != 0) throw new Exception("Cannot query the streams of " + msi);
            MsiViewExecute(view, IntPtr.Zero);
            if (MsiViewFetch(view, out record) != 0) throw new Exception("The package has no stream " + stream);
            using (var file = File.Create(destination)) {
                var buffer = new byte[1 << 20];
                while (true) {
                    uint count = (uint)buffer.Length;
                    if (MsiRecordReadStream(record, 2, buffer, ref count) != 0 || count == 0) break;
                    file.Write(buffer, 0, (int)count);
                }
            }
            MsiCloseHandle(record); MsiCloseHandle(view);
        } finally { MsiCloseHandle(db); }
    }

    static byte[] image; static uint root; static uint[] va, size, raw;
    static uint U32(long o) { return BitConverter.ToUInt32(image, (int)o); }
    static ushort U16(long o) { return BitConverter.ToUInt16(image, (int)o); }
    static uint Offset(uint rva) {
        for (int i = 0; i < va.Length; i++) if (rva >= va[i] && rva < va[i] + Math.Max(size[i], 1u)) return rva - va[i] + raw[i];
        throw new Exception("Bad RVA in the resource directory");
    }

    // path is "type/name/language", e.g. "10/DRIVER-WHQL.SYS/1033".
    public static byte[] Resource(string dll, string path) {
        image = File.ReadAllBytes(dll);
        uint pe = U32(0x3C); int sections = U16(pe + 6); int optSize = U16(pe + 20); long opt = pe + 24;
        long dirs = U16(opt) == 0x20B ? opt + 112 : opt + 96;
        va = new uint[sections]; size = new uint[sections]; raw = new uint[sections];
        for (int i = 0; i < sections; i++) { long s = opt + optSize + 40 * i; size[i] = U32(s + 8); va[i] = U32(s + 12); raw[i] = U32(s + 20); }
        root = Offset(U32(dirs + 16));
        uint dir = root;
        foreach (string part in path.Split('/')) {
            int count = U16(dir + 12) + U16(dir + 14); uint next = 0; bool found = false;
            for (int i = 0; i < count && !found; i++) {
                long e = dir + 16 + 8 * i; uint nameField = U32(e), target = U32(e + 4);
                string label;
                if ((nameField & 0x80000000) != 0) { uint n = root + (nameField & 0x7FFFFFFF); label = Encoding.Unicode.GetString(image, (int)n + 2, 2 * U16(n)); }
                else label = nameField.ToString();
                if (label == part) { next = target; found = true; }
            }
            if (!found) throw new Exception("No resource " + path + " in " + dll);
            if ((next & 0x80000000) != 0) dir = root + (next & 0x7FFFFFFF);
            else {
                uint data = root + next; uint length = U32(data + 4); uint at = Offset(U32(data));
                var bytes = new byte[length]; Array.Copy(image, at, bytes, 0, length);
                return bytes;
            }
        }
        throw new Exception("The resource " + path + " is a directory");
    }
}
'@

function Get-FileSha256([string] $Path) { (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }

function Assert-Sha256([string] $Path, [string] $Expected, [string] $What) {
    $actual = Get-FileSha256 $Path
    if ($actual -ne $Expected.ToLowerInvariant()) { throw "$What has SHA-256 $actual, but $Expected is pinned in $PinFile." }
}

function Assert-Signature([string] $Path, [string[]] $Signers) {
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -ne 'Valid') { throw "The signature of $Path is $($signature.Status): $($signature.StatusMessage)" }
    $name = $signature.SignerCertificate.GetNameInfo([System.Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
    if ($Signers -notcontains $name) { throw "$Path is signed by '$name', not by $($Signers -join ' or ')." }
}

function New-EmptyDirectory([string] $Path) {
    if (Test-Path -LiteralPath $Path) {
        if (@(Get-ChildItem -LiteralPath $Path -Force).Count -ne 0) { throw "The folder $Path is not empty." }
        return (Resolve-Path -LiteralPath $Path).Path
    }
    return (New-Item -ItemType Directory -Path $Path).FullName
}

$pin = Get-Content -LiteralPath $PinFile -Raw | ConvertFrom-Json
if (-not $WorkDirectory) { $WorkDirectory = Join-Path ([IO.Path]::GetTempPath()) ("plaitway-openvpn-" + [guid]::NewGuid().ToString('N')) }
$work = New-EmptyDirectory $WorkDirectory
$output = New-EmptyDirectory $OutputDirectory

try {
    $msi = Join-Path $work 'openvpn.msi'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $pin.url -OutFile $msi -UseBasicParsing
    Assert-Sha256 $msi $pin.msiSha256 'The downloaded installer'
    Assert-Signature $msi @($pin.signer)

    # An administrative install only writes the files below TARGETDIR.
    $image = Join-Path $work 'image'
    $process = Start-Process -FilePath 'msiexec.exe' -ArgumentList @('/a', "`"$msi`"", '/qn', "TARGETDIR=`"$image`"") -Wait -PassThru -NoNewWindow
    if ($process.ExitCode -ne 0) { throw "msiexec /a exited with $($process.ExitCode)." }
    $source = Join-Path $image 'OpenVPN'

    $bin = New-Item -ItemType Directory -Path (Join-Path $output 'bin')
    foreach ($entry in $pin.bin.PSObject.Properties) {
        $file = Join-Path $source "bin\$($entry.Name)"
        Assert-Sha256 $file $entry.Value $entry.Name
        Assert-Signature $file (@($pin.signer) + $MicrosoftSigners)
        Copy-Item -LiteralPath $file -Destination $bin.FullName
    }

    $modules = New-Item -ItemType Directory -Path (Join-Path $output 'ssl\modules')
    foreach ($entry in $pin.ssl.PSObject.Properties) {
        $file = Join-Path $source "ssl\modules\$($entry.Name)"
        Assert-Sha256 $file $entry.Value $entry.Name
        Assert-Signature $file @($pin.signer)
        Copy-Item -LiteralPath $file -Destination $modules.FullName
    }

    Copy-Item -LiteralPath (Join-Path $source 'license.txt') -Destination (Join-Path $output 'license.txt')

    $driver = New-Item -ItemType Directory -Path (Join-Path $output 'driver')
    $library = Join-Path $work 'tap-installer.dll'
    [PlaitwayOpenVpnFetch]::ExportStream($msi, $pin.tapStream, $library)
    foreach ($entry in $pin.driver.PSObject.Properties) {
        $target = Join-Path $driver.FullName $entry.Name
        [IO.File]::WriteAllBytes($target, [PlaitwayOpenVpnFetch]::Resource($library, $entry.Value.resource))
        Assert-Sha256 $target $entry.Value.sha256 $entry.Name
    }
    Assert-Signature (Join-Path $driver.FullName 'tap0901.sys') @($pin.tapPublisher)
    # The catalog is checked as a file of its own, which Windows does by the extension.
    $catalog = Get-AuthenticodeSignature -LiteralPath (Join-Path $driver.FullName 'tap0901.cat')
    if ($catalog.Status -ne 'Valid') { throw "The catalog of the driver is $($catalog.Status): $($catalog.StatusMessage)" }

    Write-Host "OpenVPN $($pin.version) (amd64), signed by $($pin.signer), with the TAP-Windows6 driver: $output"
}
finally {
    if (-not $KeepWorkDirectory) { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue }
}
