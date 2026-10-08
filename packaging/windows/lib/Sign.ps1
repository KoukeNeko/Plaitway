# Authenticode signing for the build of the installer. Dot-source this file.

Set-StrictMode -Version Latest

$script:CodeSigningUsage = '1.3.6.1.5.5.7.3.3'
$script:CertificateStores = @('Cert:\CurrentUser\My', 'Cert:\LocalMachine\My')
$script:DefaultTimestampServer = 'http://timestamp.digicert.com'

# The certificate with this thumbprint, which must be a code signing certificate whose private key is here.
function Get-SigningCertificate {
    param([Parameter(Mandatory)] [string] $Thumbprint)
    foreach ($store in $script:CertificateStores) {
        $certificate = Get-ChildItem -Path $store -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $Thumbprint } | Select-Object -First 1
        if ($null -eq $certificate) { continue }
        if (-not $certificate.HasPrivateKey) { throw "The certificate $Thumbprint has no private key here." }
        if ($certificate.EnhancedKeyUsageList.ObjectId -notcontains $script:CodeSigningUsage) { throw "The certificate $Thumbprint is not a code signing certificate." }
        return $certificate
    }
    throw "No certificate with the thumbprint $Thumbprint in $($script:CertificateStores -join ' or ')."
}

# Signs each file with SHA-256 and a timestamp, and reads the signature back: a file that is not Valid stops the build.
# A certificate that is not trusted on this machine (a self-signed one) gives UnknownError and is accepted only when
# -AllowUntrustedRoot is given, for trying the script out.
function Add-Signature {
    param(
        [Parameter(Mandatory)] [string[]] $Path,
        [Parameter(Mandatory)] $Certificate,
        [string] $TimestampServer = $script:DefaultTimestampServer,
        [switch] $AllowUntrustedRoot
    )
    foreach ($file in $Path) {
        $result = Set-AuthenticodeSignature -FilePath $file -Certificate $Certificate -HashAlgorithm SHA256 -TimestampServer $TimestampServer
        $accepted = @('Valid')
        if ($AllowUntrustedRoot) { $accepted += @('UnknownError', 'NotTrusted') }
        if ($result.Status -notin $accepted) {
            throw "Signing $file gave $($result.Status): $($result.StatusMessage)"
        }
        if ($null -eq $result.SignerCertificate -or $result.SignerCertificate.Thumbprint -ne $Certificate.Thumbprint) {
            throw "$file was not signed with the certificate $($Certificate.Thumbprint)."
        }
        Write-Host ("signed {0}" -f $file)
    }
}
