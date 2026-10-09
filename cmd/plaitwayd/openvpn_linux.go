package main

import "os"

// openvpnCandidates are where distributions put openvpn, in the order they are
// tried. $PATH is never searched: the daemon runs the binary as root, and a
// search path is something the environment decides.
var openvpnCandidates = []string{"/usr/sbin/openvpn", "/usr/bin/openvpn", "/usr/local/sbin/openvpn"}

// defaultOpenVPN is the first candidate that exists, or the first one when
// none does, so that the error names a place to put it.
func defaultOpenVPN() string { return firstExisting(openvpnCandidates) }

func firstExisting(paths []string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return paths[0]
}

// trustedOpenVPN returns the path of the openvpn binary the engines may run as
// root. A build that knows the hash of its openvpn (openvpnSHA256) runs a
// verified copy in the run directory. Otherwise the binary of the distribution
// is run where it is, provided that only root can change it or anything above
// it.
func trustedOpenVPN(source, runDir string) (string, error) {
	if openvpnSHA256 != "" {
		return installOpenVPN(source, runDir, openvpnSHA256)
	}
	return trustInPlace(source, "/", fileOwner)
}
