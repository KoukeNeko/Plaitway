<#
.SYNOPSIS
Shows what plaitwayd checks about an OpenVPN installation, without changing anything.

.DESCRIPTION
Prints, for openvpn.exe and the tapctl.exe beside it: the version, the SHA-256,
the Authenticode status and signer, and who may write to the file and to every
folder above it. Then the driver files and the adapters OpenVPN knows. The
daemon refuses a binary that a standard user could replace or that OpenVPN Inc.
did not sign; the lines marked WRITABLE and the signer line say which it is.

Needs no administrator rights and starts openvpn.exe only with --version and
--show-adapters.

.PARAMETER Binary
The openvpn.exe to look at. Default: the one in Program Files.

.EXAMPLE
.\check-openvpn.ps1
#>
[CmdletBinding()]
param(
    [string] $Binary = (Join-Path $env:ProgramFiles 'OpenVPN\bin\openvpn.exe')
)

$ErrorActionPreference = 'Stop'
$ExpectedSigner = 'OpenVPN Inc.'
# What an account must not hold. On the program and the folder it is in: any right that
# changes contents (a library next to the program is loaded by it). On the folders above:
# only the rights that put another folder in the place of the next one. A volume root
# cannot be deleted, so Delete does not count there.
$ReplaceRights = [System.Security.AccessControl.FileSystemRights]'Delete, DeleteSubdirectoriesAndFiles, ChangePermissions, TakeOwnership'
$ChangeRights = $ReplaceRights -bor [System.Security.AccessControl.FileSystemRights]'WriteData, AppendData, WriteExtendedAttributes'
$DeleteRight = [System.Security.AccessControl.FileSystemRights]'Delete'
$ContentsDepth = 2
$TrustedAccounts = @('NT AUTHORITY\SYSTEM', 'BUILTIN\Administrators', 'NT SERVICE\TrustedInstaller')

function Get-PathChain([string] $Path) {
    for ($current = $Path; $current; $current = Split-Path $current -Parent) { $current }
}

function Get-UntrustedWriter([string] $Path, [System.Security.AccessControl.FileSystemRights] $Forbidden) {
    $acl = Get-Acl -LiteralPath $Path
    if ($acl.Owner -notin $TrustedAccounts) { "owner $($acl.Owner)" }
    foreach ($rule in $acl.Access) {
        $account = $rule.IdentityReference.Value
        $inheritOnly = $rule.InheritanceFlags -ne 'None' -and $rule.PropagationFlags -eq 'InheritOnly'
        if ($rule.AccessControlType -eq 'Allow' -and $account -notin $TrustedAccounts -and -not $inheritOnly -and ($rule.FileSystemRights -band $Forbidden)) {
            "$account ($($rule.FileSystemRights))"
        }
    }
}

function Show-File([string] $Path) {
    Write-Output "== $Path"
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { Write-Output '   missing'; return }
    Write-Output "   SHA-256   $((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLower())"
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    $signer = if ($signature.SignerCertificate) { $signature.SignerCertificate.GetNameInfo('SimpleName', $false) } else { '(none)' }
    $verdict = if ($signature.Status -eq 'Valid' -and $signer -eq $ExpectedSigner) { 'ok' } else { 'REFUSED' }
    Write-Output "   signature $($signature.Status), signer $signer [$verdict]"
    $chain = @(Get-PathChain $Path)
    for ($depth = 0; $depth -lt $chain.Count; $depth++) {
        $isRoot = $depth -eq $chain.Count - 1
        $forbidden = if ($depth -lt $ContentsDepth) { $ChangeRights } elseif ($isRoot) { $ReplaceRights -band (-bnot $DeleteRight) } else { $ReplaceRights }
        foreach ($writer in Get-UntrustedWriter $chain[$depth] $forbidden) { Write-Output "   WRITABLE  $($chain[$depth]) by $writer" }
    }
}

Show-File $Binary
Show-File (Join-Path (Split-Path $Binary -Parent) 'tapctl.exe')

Write-Output '== drivers'
foreach ($driver in 'tap0901.sys', 'ovpn-dco.sys') {
    $present = Test-Path (Join-Path $env:SystemRoot "System32\drivers\$driver")
    Write-Output ("   {0,-14} {1}" -f $driver, $(if ($present) { 'installed' } else { 'missing' }))
}

if (Test-Path -LiteralPath $Binary) {
    Write-Output '== openvpn --version'
    & $Binary --version 2>&1 | Select-Object -First 3 | ForEach-Object { Write-Output "   $_" }
    Write-Output '== openvpn --show-adapters (the engine uses only adapters it made itself)'
    & $Binary --show-adapters 2>&1 | ForEach-Object { Write-Output "   $_" }
}
