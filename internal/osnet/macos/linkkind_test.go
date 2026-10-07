package macos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// Output of networksetup -listallhardwareports on macOS 27, captured read-only
// from an Apple silicon Mac with Wi-Fi (en0) and a USB Ethernet adapter
// (AX88179A, en6). The Ethernet addresses were replaced by documentation
// addresses (RFC 7042); everything else is as printed, including the blank
// first line.
const realHardwarePorts = `
Hardware Port: Ethernet Adapter (en4)
Device: en4
Ethernet Address: 00:00:5e:00:53:01

Hardware Port: Ethernet Adapter (en5)
Device: en5
Ethernet Address: 00:00:5e:00:53:02

Hardware Port: AX88179A
Device: en6
Ethernet Address: 00:00:5e:00:53:03

Hardware Port: Ethernet Adapter (en7)
Device: en7
Ethernet Address: 00:00:5e:00:53:04

Hardware Port: Thunderbolt Bridge
Device: bridge0
Ethernet Address: 00:00:5e:00:53:05

Hardware Port: Wi-Fi
Device: en0
Ethernet Address: 00:00:5e:00:53:06

Hardware Port: Thunderbolt 1
Device: en1
Ethernet Address: 00:00:5e:00:53:07

Hardware Port: Thunderbolt 2
Device: en2
Ethernet Address: 00:00:5e:00:53:08

Hardware Port: Thunderbolt 3
Device: en3
Ethernet Address: 00:00:5e:00:53:09

VLAN Configurations
===================
`

// Invented, in the format the tool prints: a USB LAN adapter, a phone over USB
// and Bluetooth next to Wi-Fi.
const inventedHardwarePorts = `
Hardware Port: Wi-Fi
Device: en0
Ethernet Address: 00:00:5e:00:53:10

Hardware Port: USB 10/100/1000 LAN
Device: en5
Ethernet Address: 00:00:5e:00:53:11

Hardware Port: iPhone USB
Device: en8
Ethernet Address: 00:00:5e:00:53:12

Hardware Port: Bluetooth PAN
Device: en9
Ethernet Address: 00:00:5e:00:53:13

VLAN Configurations
===================
`

// kindName makes failures readable: osnet.LinkKind has no String method.
func kindName(k osnet.LinkKind) string {
	switch k {
	case osnet.LinkOther:
		return "LinkOther"
	case osnet.LinkWiFi:
		return "LinkWiFi"
	case osnet.LinkEthernet:
		return "LinkEthernet"
	}
	return fmt.Sprintf("LinkKind(%d)", k)
}

func TestParseHardwarePorts(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want map[string]string
	}{
		{"real output", realHardwarePorts, map[string]string{
			"en0": "Wi-Fi", "en6": "AX88179A", "bridge0": "Thunderbolt Bridge",
			"en4": "Ethernet Adapter (en4)", "en5": "Ethernet Adapter (en5)", "en7": "Ethernet Adapter (en7)",
			"en1": "Thunderbolt 1", "en2": "Thunderbolt 2", "en3": "Thunderbolt 3",
		}},
		{"invented output", inventedHardwarePorts, map[string]string{
			"en0": "Wi-Fi", "en5": "USB 10/100/1000 LAN", "en8": "iPhone USB", "en9": "Bluetooth PAN",
		}},
		{"a port without a device is no entry", "Hardware Port: Wi-Fi\n\nHardware Port: Bluetooth PAN\nDevice: en3\n", map[string]string{"en3": "Bluetooth PAN"}},
		{"nothing", "", map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHardwarePorts(tt.out)
			if len(got) != len(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			for device, port := range tt.want {
				if got[device] != port {
					t.Errorf("%s is %q, want %q", device, got[device], port)
				}
			}
		})
	}
}

func TestClassifyPort(t *testing.T) {
	tests := []struct {
		port string
		want osnet.LinkKind
	}{
		// The ports of the machine the fixture comes from.
		{"Wi-Fi", osnet.LinkWiFi},
		{"AX88179A", osnet.LinkEthernet}, // a USB adapter named after its chip
		{"Ethernet Adapter (en4)", osnet.LinkEthernet},
		{"Thunderbolt Bridge", osnet.LinkOther},
		{"Thunderbolt 1", osnet.LinkOther},
		// Invented.
		{"AirPort", osnet.LinkWiFi},
		{"Ethernet", osnet.LinkEthernet},
		{"USB 10/100/1000 LAN", osnet.LinkEthernet},
		{"Belkin USB-C LAN", osnet.LinkEthernet},
		{"Thunderbolt Ethernet Slot 1", osnet.LinkEthernet},
		{"RTL8153", osnet.LinkEthernet},
		{"iPhone USB", osnet.LinkOther},
		{"iPad USB", osnet.LinkOther},
		{"Bluetooth PAN", osnet.LinkOther},
		{"FireWire", osnet.LinkOther},
	}
	for _, tt := range tests {
		if got := classifyPort(tt.port); got != tt.want {
			t.Errorf("classifyPort(%q) = %s, want %s", tt.port, kindName(got), kindName(tt.want))
		}
	}
}

// portsRunner answers networksetup with a canned output and counts the calls.
type portsRunner struct {
	mu   sync.Mutex
	out  string
	err  error
	runs int
}

func (r *portsRunner) run(_ context.Context, name string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name != networksetupPath || !slices.Equal(args, []string{"-listallhardwareports"}) {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	r.runs++
	return []byte(r.out), r.err
}

func (r *portsRunner) set(out string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out, r.err = out, err
}

func (r *portsRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs
}

func TestLinkKindsOfTheRealPorts(t *testing.T) {
	r := &portsRunner{out: realHardwarePorts}
	k := newLinkKinds(r.run, discardLog())
	tests := []struct {
		device string
		want   osnet.LinkKind
	}{
		{"en0", osnet.LinkWiFi},
		{"en6", osnet.LinkEthernet},
		{"en1", osnet.LinkOther},
		{"bridge0", osnet.LinkOther},
		{"utun10", osnet.LinkOther}, // not a hardware port
		{"", osnet.LinkOther},
	}
	for _, tt := range tests {
		if got := k.kind(tt.device); got != tt.want {
			t.Errorf("kind(%q) = %s, want %s", tt.device, kindName(got), kindName(tt.want))
		}
	}
}

// The table is read when an interface shows up that it does not list, not for
// every question.
func TestLinkKindsReadTheTableWhenAnInterfaceAppears(t *testing.T) {
	r := &portsRunner{out: "Hardware Port: Wi-Fi\nDevice: en0\n"}
	k := newLinkKinds(r.run, discardLog())
	for range 3 {
		if got := k.kind("en0"); got != osnet.LinkWiFi {
			t.Fatalf("en0 is %s", kindName(got))
		}
	}
	if r.count() != 1 {
		t.Fatalf("the table was read %d times for one interface", r.count())
	}

	// A USB LAN adapter is plugged in: the table has it now.
	r.set("Hardware Port: Wi-Fi\nDevice: en0\n\nHardware Port: USB 10/100/1000 LAN\nDevice: en5\n", nil)
	for range 3 {
		if got := k.kind("en5"); got != osnet.LinkEthernet {
			t.Fatalf("en5 is %s", kindName(got))
		}
	}
	if got := k.kind("en0"); got != osnet.LinkWiFi {
		t.Errorf("en0 is %s after the second read", kindName(got))
	}
	if r.count() != 2 {
		t.Errorf("the table was read %d times, want 2", r.count())
	}

	// An interface the table never lists is neither Wi-Fi nor Ethernet.
	if got := k.kind("vmenet0"); got != osnet.LinkOther {
		t.Errorf("vmenet0 is %s", kindName(got))
	}
}

// Snapshot is on the Reconciler's path: a tool that fails or hangs must not be
// run again for every call, and a failure must not lose what is known.
func TestLinkKindsDoNotRepeatAFailingTool(t *testing.T) {
	r := &portsRunner{out: realHardwarePorts}
	k := newLinkKinds(r.run, discardLog())
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	k.now = func() time.Time { return now }
	if got := k.kind("en0"); got != osnet.LinkWiFi {
		t.Fatalf("en0 is %s", kindName(got))
	}

	r.set("", errors.New("exit status 1"))
	if got := k.kind("en9"); got != osnet.LinkOther {
		t.Errorf("an interface that cannot be looked up is %s, want LinkOther", kindName(got))
	}
	if got := k.kind("en0"); got != osnet.LinkWiFi {
		t.Errorf("a failed read lost the table: en0 is %s", kindName(got))
	}
	now = now.Add(linkRetry - time.Second)
	k.kind("en9")
	k.kind("en9")
	if r.count() != 2 {
		t.Errorf("the tool ran %d times within %v of its failure, want 2", r.count(), linkRetry)
	}

	// It works again, and the next interface is looked up once the wait is over.
	r.set(realHardwarePorts+"\nHardware Port: USB 10/100/1000 LAN\nDevice: en9\n", nil)
	now = now.Add(time.Second)
	if got := k.kind("en9"); got != osnet.LinkEthernet {
		t.Errorf("en9 is %s after the tool recovered", kindName(got))
	}
}

// A tool that prints nothing we understand is a failure, not an empty network.
func TestLinkKindsTreatAnOutputWithoutPortsAsAFailure(t *testing.T) {
	r := &portsRunner{out: "networksetup: something new\n"}
	k := newLinkKinds(r.run, discardLog())
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	k.now = func() time.Time { return now }
	for range 3 {
		if got := k.kind("en0"); got != osnet.LinkOther {
			t.Fatalf("en0 is %s", kindName(got))
		}
	}
	if r.count() != 1 {
		t.Errorf("the tool ran %d times, want 1 and then the wait", r.count())
	}
}

// Snapshot is called from more than one goroutine: the Reconciler's and the
// one that applies on-demand rules.
func TestLinkKindsAreSafeForConcurrentUse(t *testing.T) {
	r := &portsRunner{out: realHardwarePorts}
	k := newLinkKinds(r.run, discardLog())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				for device, want := range map[string]osnet.LinkKind{"en0": osnet.LinkWiFi, "en6": osnet.LinkEthernet, "utun3": osnet.LinkOther} {
					if got := k.kind(device); got != want {
						t.Errorf("kind(%q) = %s, want %s", device, kindName(got), kindName(want))
						return
					}
				}
			}
		})
	}
	wg.Wait()
}
