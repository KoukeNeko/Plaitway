package wg

import "github.com/KoukeNeko/Plaitway/internal/tunnel"

// defaultAdapterPrefix starts the name of every Windows adapter the daemon
// creates, so that the adapters left by a crashed daemon can be told from
// anybody else's. Nothing else uses it.
const defaultAdapterPrefix = "Plaitway-"

// logFunc is how code below the engine reports to the daemon's log.
type logFunc func(level tunnel.LogLevel, text string)

// What differs between the systems lives in platform_<os>.go, behind these
// functions:
//
//	createPlatformTun(owner, adapterPrefix, mtu, log)  the tunnel device
//	platformInterfaces(dev)                            addresses and MTU
//	platformBind()                                     wireguard-go's UDP socket
//	checkPlatform()                                    what Probe still needs
//	platformVersion()                                  the tail of Probe's version
//	awaitInterfaceRemoval(name, log)                   the end of Stop

// newBind makes the UDP socket of a device. It is a variable only so that the
// tests on Windows can listen on loopback: wireguard-go's own sockets listen on
// every address, and Windows Defender Firewall then asks the person at the
// keyboard about each new test binary. Nothing else assigns it.
var newBind = platformBind
