//go:build !windows && !linux

package main

import (
	"os"
	"path/filepath"
)

// defaultOpenVPN is where the app bundle keeps openvpn:
// Plaitway.app/Contents/Resources/bin/openvpn next to Contents/MacOS/plaitwayd.
func defaultOpenVPN() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "Resources", "bin", "openvpn")
}

// trustedOpenVPN returns the path of the openvpn binary the engines may run
// as root. A build that knows the hash of its openvpn (openvpnSHA256) runs a
// verified copy in the run directory; a development build, which has none,
// runs the configured path as it is.
func trustedOpenVPN(source, runDir string) (string, error) {
	if openvpnSHA256 == "" {
		return source, nil
	}
	return installOpenVPN(source, runDir, openvpnSHA256)
}
