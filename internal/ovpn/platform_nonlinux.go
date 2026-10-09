//go:build !linux

package ovpn

// maxSocketPath is the longest Unix socket path macOS accepts (sun_path is
// 104 bytes including the terminating NUL).
const maxSocketPath = 103

// tapRefusal is why a profile that asks for a tap device cannot connect. The
// warning of Parse says it; openvpn itself fails the start (refusesTap).
const tapRefusal = "macOS has no tap device"

// refusesTap says whether the engine itself refuses to start a profile that
// asks for a tap device.
const refusesTap = false
