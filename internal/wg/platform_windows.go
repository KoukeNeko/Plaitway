package wg

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
	"github.com/KoukeNeko/Plaitway/internal/winiface"
)

const (
	// adapterTunnelType is the type Windows shows for the adapters.
	adapterTunnelType = "Plaitway"

	// adapterRemovalTimeout bounds the wait for a closed adapter to vanish from
	// the interface list; removal is asked for by Close but done by the system.
	adapterRemovalTimeout = 5 * time.Second
	adapterRemovalPoll    = 50 * time.Millisecond
)

func init() {
	tun.WintunTunnelType = adapterTunnelType
}

// cleanedPrefixes holds the adapter prefixes whose leftovers were removed in
// this process. The removal runs before the first adapter of a prefix is
// created and never after: from then on every adapter with that prefix may
// belong to a running tunnel.
var cleanedPrefixes = struct {
	sync.Mutex
	done map[string]bool
}{done: map[string]bool{}}

// createPlatformTun creates the wintun adapter of the profile, named after it
// and with a GUID derived from it, so that the adapter is the same network
// every time. Creating the first adapter installs the wintun driver, which
// needs an elevated process.
func createPlatformTun(owner tunnel.OwnerID, adapterPrefix string, mtu int, log logFunc) (tun.Device, error) {
	if err := ensureWintun(); err != nil {
		return nil, err
	}
	removeLeftoversOnce(adapterPrefix, log)

	guid := adapterGUID(owner)
	dev, err := tun.CreateTUNWithRequestedGUID(adapterName(adapterPrefix, owner), &guid, mtu)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, fmt.Errorf("%w (creating an adapter needs an elevated process)", err)
	}
	return dev, err
}

// removeLeftoversOnce removes the adapters a crashed daemon left behind. A
// failure only costs the name of an adapter that is still there, which the
// creation then reports, so it is logged and not returned.
func removeLeftoversOnce(prefix string, log logFunc) {
	cleanedPrefixes.Lock()
	defer cleanedPrefixes.Unlock()
	if cleanedPrefixes.done[prefix] {
		return
	}
	cleanedPrefixes.done[prefix] = true
	removed, err := removeStaleAdapters(prefix)
	for _, name := range removed {
		log(tunnel.LogInfo, "removed leftover adapter "+name)
	}
	if err != nil {
		log(tunnel.LogWarn, "remove leftover adapters: "+err.Error())
	}
}

func platformInterfaces(dev tun.Device) Interfaces {
	return windowsInterfaces{luid: nativeLUID(dev)}
}

// platformBind is wireguard-go's default for Windows: sockets on registered I/O,
// and the standard ones when the system lacks it. Both are bound to the
// wildcard address and are opened again, on the same port, by BindUpdate.
func platformBind() conn.Bind { return conn.NewDefaultBind() }

// checkPlatform: the engine needs a wintun.dll it can trust.
func checkPlatform() error { return ensureWintun() }

func platformVersion() string {
	if version := wintunVersion(); version != "" {
		return "wintun " + version
	}
	return ""
}

// awaitInterfaceRemoval waits until the closed adapter is out of the interface
// list, so that Stop returns with the adapter gone and the next Start of the
// same profile can create its adapter under the same name.
func awaitInterfaceRemoval(name string, log logFunc) {
	if name == "" {
		return
	}
	waitForRemoval(name, log, isInterfaceListed, adapterRemovalTimeout, adapterRemovalPoll)
}

// waitForRemoval polls listed until it says the interface is gone, or the
// timeout has passed, which is logged and not an error: the adapter is closed
// either way.
func waitForRemoval(name string, log logFunc, listed func(string) bool, timeout, poll time.Duration) {
	deadline := time.Now().Add(timeout)
	for listed(name) {
		if time.Now().After(deadline) {
			log(tunnel.LogWarn, fmt.Sprintf("adapter %s is still listed %s after it was closed", name, timeout))
			return
		}
		time.Sleep(poll)
	}
}

// isInterfaceListed is replaced by tests of the engine.
var isInterfaceListed = interfaceExists

func interfaceExists(name string) bool {
	_, err := winiface.FindByName(name)
	return err == nil
}
