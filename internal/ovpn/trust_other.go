//go:build !windows

package ovpn

import "errors"

// VerifyBinary exists on Windows only. Elsewhere the daemon copies the binary
// into its run directory and checks it there; see cmd/plaitwayd.
func VerifyBinary(path, wantSHA256 string) error {
	return errors.New("VerifyBinary is for Windows; other systems verify a copy of the binary in the run directory")
}
