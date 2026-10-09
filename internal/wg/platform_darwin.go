package wg

import (
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// utunName asks the kernel for the next free utun device.
const utunName = "utun"

// createPlatformTun creates a utun device; the owner and the adapter prefix
// matter only to Windows, where the adapter keeps a name between runs.
func createPlatformTun(_ tunnel.OwnerID, _ string, mtu int, _ logFunc) (tun.Device, error) {
	return tun.CreateTUN(utunName, mtu)
}

func platformInterfaces(tun.Device) Interfaces { return ifconfig{} }

func platformBind() conn.Bind { return conn.NewStdNetBind() }

// checkPlatform: wireguard-go is linked into the daemon and a utun needs
// nothing installed.
func checkPlatform() error { return nil }

// platformVersion is what follows the wireguard-go version in the engine's
// version string; nothing else is part of the engine here.
func platformVersion() string { return "" }

// awaitInterfaceRemoval: closing a utun device destroys it before Close returns.
func awaitInterfaceRemoval(string, logFunc) {}
