package wg

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/KoukeNeko/Plaitway/internal/osnet/linux"
	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// tunNamePattern is completed by the kernel: %d becomes the first number no
// interface has. The device is not persistent, so it is gone when the file
// descriptor closes, also after a crash, and a name never outlives its owner.
const tunNamePattern = "plaitway%d"

// These are variables only for the tests.
var (
	// tunDevicePath is the clone device of the kernel's tun driver.
	tunDevicePath = "/dev/net/tun"
	// procSysNet holds the sysctls of the network namespace of the process.
	procSysNet = "/proc/sys/net"
	// removalWait and removalPoll bound and pace awaitInterfaceRemoval.
	removalWait = 5 * time.Second
	removalPoll = 10 * time.Millisecond
)

// createPlatformTun creates a tun device named by the kernel; the owner and the
// adapter prefix matter only to Windows, where the adapter keeps a name between
// runs.
func createPlatformTun(_ tunnel.OwnerID, _ string, mtu int, log logFunc) (tun.Device, error) {
	dev, err := tun.CreateTUN(tunNamePattern, mtu)
	if err != nil {
		return nil, explainTunError(err)
	}
	if name, err := dev.Name(); err == nil {
		enableIPv6(name, log)
	}
	return dev, nil
}

// explainTunError says what is missing when the tun device cannot be created.
// wireguard-go reports a missing /dev/net/tun without the error behind it, so
// the device is opened again to see why.
func explainTunError(err error) error {
	if probe := checkTunDevice(tunDevicePath); probe != nil {
		return probe
	}
	if errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("creating a network device needs CAP_NET_ADMIN: %w", err)
	}
	return err
}

// checkTunDevice opens the tun clone device, which creates nothing until an
// interface is attached to the descriptor, and says why it cannot be used.
func checkTunDevice(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return tunOpenError(path, err)
	}
	return f.Close()
}

func tunOpenError(path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist: load the tun kernel module (modprobe tun), or give the container the device", path)
	case errors.Is(err, syscall.ENODEV):
		return fmt.Errorf("the kernel has no tun driver behind %s: load the tun module (modprobe tun)", path)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("no permission to open %s: the daemon needs root, and a service or container has to be allowed to use the device: %w", path, err)
	}
	return err
}

// enableIPv6 turns IPv6 on for an interface that the host's defaults created
// with it off (net.ipv6.conf.default.disable_ipv6=1). The kernel then refuses
// the interface's IPv6 address and every IPv6 route through it, and the
// profile asks for them. Failing to do it is only logged: an IPv4 profile does
// not need it, and for the others the kernel's own refusal names the cause. A
// host without an IPv6 stack, or an interface the kernel took IPv6 away from
// for its MTU, has nothing to enable.
func enableIPv6(name string, log logFunc) {
	path := filepath.Join(procSysNet, "ipv6", "conf", name, "disable_ipv6")
	state, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) || strings.TrimSpace(string(state)) == "0":
		return
	case err != nil:
		log(tunnel.LogWarn, fmt.Sprintf("read whether IPv6 is disabled on %s: %v", name, err))
		return
	}
	if err := os.WriteFile(path, []byte("0"), 0); err != nil {
		log(tunnel.LogWarn, fmt.Sprintf("enable IPv6 on %s, which the host disables by default: %v", name, err))
		return
	}
	log(tunnel.LogInfo, "IPv6 is disabled by default on this host; enabled it on "+name)
}

// netlinkInterfaces is the default Interfaces: addresses, MTU and link state
// over rtnetlink, which needs CAP_NET_ADMIN.
type netlinkInterfaces struct{}

func (netlinkInterfaces) Configure(_ context.Context, name string, addrs []netip.Prefix, mtu int) error {
	return linux.ConfigureLink(name, addrs, mtu)
}

func platformInterfaces(tun.Device) Interfaces { return netlinkInterfaces{} }

// platformBind: wireguard-go's standard bind sends through the kernel's routing
// table, so the packets of a full tunnel need the Reconciler's host route to
// the endpoint and no socket mark or policy rule. Its sockets are sticky: they
// answer from the address and interface a peer last wrote to. wireguard-go
// forgets those on a change of the IPv4 routes by itself and on BindUpdate,
// which is what Rebind calls, for the rest.
func platformBind() conn.Bind { return conn.NewDefaultBind() }

// checkPlatform: wireguard-go is linked into the daemon, so all it needs is the
// kernel's tun driver.
func checkPlatform() error { return checkTunDevice(tunDevicePath) }

// platformVersion is what follows the wireguard-go version in the engine's
// version string; nothing else is part of the engine here.
func platformVersion() string { return "" }

// awaitInterfaceRemoval waits until no interface has the name. Closing a tun
// device destroys its interface while the descriptor closes, so this is a
// safety net for the promise that Stop makes. A new interface that took the name
// in between keeps it waiting until removalWait is over.
func awaitInterfaceRemoval(name string, log logFunc) {
	deadline := time.Now().Add(removalWait)
	for {
		gone, err := interfaceGone(name)
		if err != nil {
			log(tunnel.LogWarn, fmt.Sprintf("check that %s is gone: %v", name, err))
			return
		}
		if gone {
			return
		}
		if time.Now().After(deadline) {
			log(tunnel.LogWarn, fmt.Sprintf("%s is still there %s after its device was closed", name, removalWait))
			return
		}
		time.Sleep(removalPoll)
	}
}

// interfaceGone reports whether no interface has this name.
func interfaceGone(name string) (bool, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	req, err := unix.NewIfreq(name)
	if err != nil {
		return false, err
	}
	switch err := unix.IoctlIfreq(fd, unix.SIOCGIFINDEX, req); {
	case err == nil:
		return false, nil
	case errors.Is(err, unix.ENODEV):
		return true, nil
	default:
		return false, err
	}
}
