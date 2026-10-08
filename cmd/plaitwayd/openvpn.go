package main

import "github.com/KoukeNeko/Plaitway/internal/tunnel"

// What each OS asks of its openvpn binary is in openvpn_other.go (macOS and
// Linux: a verified copy in the run directory) and openvpn_windows.go (the
// binary is checked where it is). Both give the daemon the same three things:
// defaultOpenVPN for the -openvpn flag, and trustedOpenVPN for selectEngines.

// openvpnUnavailable makes the OpenVPN backend report why it cannot be used,
// which the profile's last_error and DaemonInfo show.
func openvpnUnavailable(backends []tunnel.Backend, reason error) []tunnel.Backend {
	for i := range backends {
		if backends[i].Kind == tunnel.KindOpenVPN {
			backends[i].Probe = func() tunnel.EngineInfo { return tunnel.EngineInfo{Detail: reason.Error()} }
		}
	}
	return backends
}
