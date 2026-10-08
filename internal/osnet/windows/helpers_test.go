package windows

import (
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func pfx(texts ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(texts))
	for i, text := range texts {
		out[i] = netip.MustParsePrefix(text)
	}
	return out
}

func ip(text string) netip.Addr { return netip.MustParseAddr(text) }

// manualClock is a clock that moves only when the test says so, and timers that
// fire only when the test fires them: fire(d) fires the oldest timer set for d,
// waiting for the code under test to set it.
type manualClock struct {
	mu        sync.Mutex
	wall      time.Time
	mono      time.Duration
	monoReads int
	timers    []manualTimer
}

type manualTimer struct {
	d  time.Duration
	ch chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{wall: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *manualClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.monoReads++
	return c.mono
}

// reads is how often Mono was read: a way to know that the code under test has
// looked at the clock.
func (c *manualClock) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.monoReads
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := manualTimer{d: d, ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += d
	c.wall = c.wall.Add(d)
}

func (c *manualClock) fire(t testing.TB, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		for i, timer := range c.timers {
			if timer.d == d {
				c.timers = slices.Delete(c.timers, i, i+1)
				timer.ch <- c.wall
				c.mu.Unlock()
				return
			}
		}
		pending := make([]time.Duration, len(c.timers))
		for i, timer := range c.timers {
			pending[i] = timer.d
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no timer set for %v; pending: %v", d, pending)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitPending waits until a timer is set for d, without firing it.
func (c *manualClock) waitPending(t testing.TB, d time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		c.mu.Lock()
		found := slices.ContainsFunc(c.timers, func(timer manualTimer) bool { return timer.d == d })
		c.mu.Unlock()
		if found {
			return
		}
	}
	t.Fatalf("no timer set for %v", d)
}

// Adapters as the machine this was written on reports them (interface type,
// driver description, hardware flag), plus the ones a user of Plaitway is likely
// to have. They are the fixtures of the classification and NetState tests.
var (
	intelEthernet = adapter{LUID: 0x1002, Index: 2, Name: "乙太網路 2", Description: "Intel(R) Ethernet Controller (3) I225-V", Type: ifTypeEthernet, Hardware: true, Up: true, Metric4: 25, Metric6: 25,
		Addrs: pfx("192.168.1.101/24", "fe80::316b:5a55:6fe7:c264/64", "2001:b011:c006:bf71:eadc:7f5d:62c2:859f/64", "2001:b011:c006:bf71:146d:7272:59ac:1e1d/128")}
	realtekWiFi = adapter{LUID: 0x1017, Index: 23, Name: "Wi-Fi", Description: "Realtek 8821CU Wireless LAN 802.11ac USB NIC", Type: ifTypeIEEE80211, Hardware: true, Up: true, Metric4: 40, Metric6: 40,
		Addrs: pfx("10.20.0.7/24")}
	openVPNTap = adapter{LUID: 0x1014, Index: 20, Name: "OpenVPN TAP-Windows6", Description: "TAP-Windows Adapter V9", Type: ifTypePropVirtual, Up: true, Metric4: 25, Metric6: 25,
		Addrs: pfx("10.200.0.2/16")}
	openVPNDCO     = adapter{LUID: 0x100e, Index: 14, Name: "OpenVPN Data Channel Offload", Description: "OpenVPN Data Channel Offload", Type: ifTypePropVirtual, Up: true, Metric4: 25}
	hyperVSwitch   = adapter{LUID: 0x1016, Index: 22, Name: "vEthernet (Default Switch)", Description: "Hyper-V Virtual Ethernet Adapter", Type: ifTypeEthernet, Up: true, Metric4: 15, Metric6: 15, Addrs: pfx("172.21.128.1/20")}
	bluetoothPAN   = adapter{LUID: 0x100d, Index: 13, Name: "藍牙網路連線", Description: "Bluetooth Device (Personal Area Network)", Type: ifTypeEthernet, Up: true, Metric4: 65}
	usbNCM         = adapter{LUID: 0x1004, Index: 4, Name: "乙太網路 4", Description: "UsbNcm Host Device", Type: ifTypeEthernet, Hardware: true, Up: true, Metric4: 25}
	teredo         = adapter{LUID: 0x1010, Index: 16, Name: "Teredo Tunneling Pseudo-Interface", Description: "Microsoft Teredo Tunneling Adapter", Type: ifTypeTunnel, Up: true}
	wanMiniportIKE = adapter{LUID: 0x1011, Index: 17, Name: "區域連線* 2", Description: "WAN Miniport (IKEv2)", Type: ifTypeTunnel, Up: true}
	wanPPPoE       = adapter{LUID: 0x1019, Index: 25, Name: "區域連線* 5", Description: "WAN Miniport (PPPOE)", Type: ifTypePPP, Up: true}
	loopback       = adapter{LUID: 0x1001, Index: 1, Name: "Loopback Pseudo-Interface 1", Description: "Software Loopback Interface 1", Type: ifTypeLoopback, Up: true, Metric4: 75, Metric6: 75,
		Addrs: pfx("127.0.0.1/8", "::1/128")}
	wintun     = adapter{LUID: 0x2001, Index: 60, Name: "wg0", Description: "Wintun Userspace Tunnel", Type: ifTypeEthernet, Up: true, Metric4: 5, Addrs: pfx("10.6.0.2/24")}
	wireGuard  = adapter{LUID: 0x2002, Index: 61, Name: "Plaitway-1", Description: "WireGuard Tunnel", Type: ifTypePropVirtual, Up: true}
	anyConnect = adapter{LUID: 0x2003, Index: 62, Name: "Ethernet 3", Description: "Cisco AnyConnect Secure Mobility Client Virtual Miniport Adapter for Windows x64", Type: ifTypeEthernet, Hardware: true, Up: true}
	tailscale  = adapter{LUID: 0x2004, Index: 63, Name: "Tailscale", Description: "Tailscale Tunnel", Type: ifTypeEthernet, Up: true}
	appleUSB   = adapter{LUID: 0x2005, Index: 64, Name: "Ethernet 5", Description: "Apple Mobile Device Ethernet", Type: ifTypeEthernet, Hardware: true, Up: true}
	rndisPhone = adapter{LUID: 0x2006, Index: 65, Name: "Ethernet 6", Description: "Remote NDIS based Internet Sharing Device", Type: ifTypeEthernet, Hardware: true, Up: true}
	wifiDirect = adapter{LUID: 0x2007, Index: 66, Name: "Local Area Connection* 10", Description: "Microsoft Wi-Fi Direct Virtual Adapter #2", Type: ifTypeIEEE80211, Hardware: true, Up: true}
	usbGigabit = adapter{LUID: 0x2008, Index: 67, Name: "Ethernet 7", Description: "Realtek USB GbE Family Controller", Type: ifTypeEthernet, Hardware: true, Up: true}
	wwan       = adapter{LUID: 0x2009, Index: 68, Name: "Cellular", Description: "Quectel Mobile Broadband Adapter", Type: 243, Hardware: true, Up: true}
)

func defaultRoute(dst, gateway string, a adapter, metric uint32) osnet.Route {
	r := osnet.Route{Dst: netip.MustParsePrefix(dst), Iface: a.Name, IfIndex: a.Index, Metric: metric, Static: true}
	if gateway != "" {
		r.Gateway = netip.MustParseAddr(gateway)
	}
	return r
}
