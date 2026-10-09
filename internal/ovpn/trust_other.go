//go:build !windows

package ovpn

import "errors"

// VerifyBinary exists on Windows only. Elsewhere the daemon decides which binary
// it trusts before it gives the path to the engine; see cmd/plaitwayd.
func VerifyBinary(path, wantSHA256 string) error {
	return errors.New("VerifyBinary is for Windows; other systems verify a copy of the binary in the run directory")
}
