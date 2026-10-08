// Package authenticode checks the embedded Authenticode signature of a file
// and who signed it. The daemon runs as LocalSystem and loads or starts files
// that other software installed (wintun.dll, openvpn.exe, tapctl.exe), so it
// holds them to a signature of the publisher it expects. The check itself is
// Windows only (authenticode_windows.go).
//
// What counts as valid, and why:
//
//   - The signature is the one embedded in the file. A file that is trusted
//     through a catalog (most of Windows itself) has none and is refused: the
//     publishers checked here sign their files.
//   - Revocation is not checked. The daemon starts at boot, possibly before the
//     network is up, and a revocation answer tells little about a file whose
//     certificate has expired since it was signed.
//   - An expired certificate is accepted when the signature carries a trusted
//     timestamp from the time the certificate was valid. wintun.dll is signed
//     that way, and the signature stays valid for as long as the timestamp's
//     authority is trusted.
//   - The common name of the signer's certificate must be the name the caller
//     expects, so that a file signed by another publisher the machine trusts
//     (Microsoft, say) is refused.
package authenticode
