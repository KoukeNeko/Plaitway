//go:build rootintegration

package winiface

// These tests change addresses and the interface metric of the loopback
// pseudo-interface, so they refuse to run in a process that is not elevated.
// From an elevated shell:
//
//	go test -tags rootintegration -count=1 -run TestRoot -v ./internal/winiface
//
// What they add is the address 192.0.2.77/32 and 2001:db8::77/128 (documentation
// ranges); the interface metric is put back as it was. All of it is undone
// again, also when a test fails, and also when it is killed: a guard process
// (internal/winiface/rootguard, this test binary started again) is armed before
// the first change and puts the loopback back when this process ends without
// releasing it, or when its deadline passes. Each guard prints the command that
// does by hand what it does.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/winiface/rootguard"
)

func requireElevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("refusing to run: this test changes the host; run it from an elevated shell")
	}
}

const (
	recoveryLoopback    = "loopback"
	loopbackGuardWindow = 2 * time.Minute
)

// savedMetric is the interface metric of one family as it was before a test.
type savedMetric struct {
	Family Family
	Metric uint32
	Auto   bool
}

// loopbackRecovery is what puts the loopback back as it was: the addresses to
// take off and the metrics to give back. It is the argument of the guard, as
// JSON.
type loopbackRecovery struct {
	Addresses []netip.Addr
	Metrics   []savedMetric
}

func init() { rootguard.Register(recoveryLoopback, restoreLoopback) }

// TestRootGuardHelper is the entry of the guards that these tests arm: the
// test binary started again (see internal/winiface/rootguard).
func TestRootGuardHelper(t *testing.T) { rootguard.RunHelper(t) }

// restoreLoopback is the recovery, and the clean-up of the tests as well: both
// do the same, and the clean-up must not differ from what the guard does.
func restoreLoopback(argument string) error {
	var recovery loopbackRecovery
	if err := json.Unmarshal([]byte(argument), &recovery); err != nil {
		return fmt.Errorf("read the recovery: %w", err)
	}
	link, err := FindByName(loopbackAlias)
	if err != nil {
		return err
	}
	var failures []error
	for _, address := range recovery.Addresses {
		if err := host.deleteAddress(link.LUID, address); err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", address, err))
		}
	}
	for _, saved := range recovery.Metrics {
		err := host.changeIPInterface(link.LUID, saved.Family, func(row *ipInterface) { row.Metric, row.AutoMetric = saved.Metric, saved.Auto })
		if err != nil {
			failures = append(failures, fmt.Errorf("restore the %s metric: %w", saved.Family, err))
		}
	}
	return errors.Join(failures...)
}

// manualCommand does by hand what restoreLoopback does.
func (r loopbackRecovery) manualCommand() string {
	var commands []string
	if len(r.Addresses) > 0 {
		addresses := make([]string, len(r.Addresses))
		for i, address := range r.Addresses {
			addresses[i] = address.String()
		}
		commands = append(commands, fmt.Sprintf("Remove-NetIPAddress -InterfaceAlias '%s' -IPAddress %s -Confirm:$false", loopbackAlias, strings.Join(addresses, ",")))
	}
	for _, saved := range r.Metrics {
		if saved.Auto {
			commands = append(commands, fmt.Sprintf("Set-NetIPInterface -InterfaceAlias '%s' -AddressFamily %s -AutomaticMetric Enabled", loopbackAlias, saved.Family))
		} else {
			commands = append(commands, fmt.Sprintf("Set-NetIPInterface -InterfaceAlias '%s' -AddressFamily %s -AutomaticMetric Disabled -InterfaceMetric %d", loopbackAlias, saved.Family, saved.Metric))
		}
	}
	return strings.Join(commands, "; ")
}

// guardLoopback arms the guard that puts the loopback back if this process is
// killed, and registers the clean-up that does the same when the test ends. Call
// it before the first change.
func guardLoopback(t *testing.T, recovery loopbackRecovery) {
	t.Helper()
	argument, err := json.Marshal(recovery)
	if err != nil {
		t.Fatal(err)
	}
	rootguard.Arm(t, rootguard.Plan{Action: recoveryLoopback, Argument: string(argument), Within: loopbackGuardWindow, Manual: recovery.manualCommand()})
	t.Cleanup(func() {
		if err := restoreLoopback(string(argument)); err != nil {
			t.Errorf("clean up the loopback: %v", err)
		}
	})
}

// replacementAddress is the address the second half of the address test sets in
// place of the scratch ones.
var replacementAddress = netip.MustParsePrefix("192.0.2.78/32")

func scratchAddresses() []netip.Prefix {
	return []netip.Prefix{netip.MustParsePrefix("192.0.2.77/32"), netip.MustParsePrefix("2001:db8::77/128")}
}

// scratchAddressesToRemove are the addresses a test may have added, the one of
// the replacement included.
func scratchAddressesToRemove() []netip.Addr {
	var addresses []netip.Addr
	for _, p := range append(scratchAddresses(), replacementAddress) {
		addresses = append(addresses, p.Addr())
	}
	return addresses
}

// manualAddresses are the addresses of the link that somebody configured, with
// the loopback ones, which every Windows has.
func manualAddresses(t *testing.T, link Link) []netip.Prefix {
	t.Helper()
	all, err := host.addresses(link.LUID)
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Prefix
	for _, a := range all {
		if a.Manual {
			out = append(out, a.Prefix)
		}
	}
	slices.SortFunc(out, comparePrefix)
	return out
}

func loopbackLink(t *testing.T) Link {
	t.Helper()
	link, err := FindByName(loopbackAlias)
	if err != nil {
		t.Fatal(err)
	}
	return link
}

func TestRootSetAddressesOnLoopback(t *testing.T) {
	requireElevated(t)
	link := loopbackLink(t)
	original := manualAddresses(t, link)
	for _, p := range append(scratchAddresses(), replacementAddress) {
		if slices.Contains(original, p) {
			t.Fatalf("%s is on the loopback before the test", p)
		}
	}
	guardLoopback(t, loopbackRecovery{Addresses: scratchAddressesToRemove()})

	has := func(want ...netip.Prefix) {
		t.Helper()
		got := manualAddresses(t, link)
		for _, p := range original {
			if !slices.Contains(got, p) {
				t.Errorf("the loopback lost %s", p)
			}
		}
		for _, p := range want {
			if !slices.Contains(got, p) {
				t.Errorf("%s is missing; addresses: %v", p, got)
			}
		}
		if len(got) != len(original)+len(want) {
			t.Errorf("addresses = %v, want %v and %v", got, original, want)
		}
	}

	if err := link.SetAddresses(scratchAddresses()); err != nil {
		t.Fatalf("SetAddresses: %v", err)
	}
	has(scratchAddresses()...)
	// The new addresses are usable when the call returns.
	for _, p := range scratchAddresses() {
		if state, err := host.addressState(link.LUID, p.Addr()); err != nil || state != dadPreferred {
			t.Errorf("%s is in state %d (%v) after SetAddresses", p.Addr(), state, err)
		}
	}
	if err := link.SetAddresses(scratchAddresses()); err != nil {
		t.Fatalf("second SetAddresses: %v", err)
	}
	has(scratchAddresses()...)

	replacement := replacementAddress
	if err := link.SetAddresses([]netip.Prefix{replacement}); err != nil {
		t.Fatalf("replacing SetAddresses: %v", err)
	}
	has(replacement)

	if err := link.SetAddresses(nil); err != nil {
		t.Fatalf("clearing SetAddresses: %v", err)
	}
	has()
}

func TestRootSetInterfaceMetricOnLoopback(t *testing.T) {
	requireElevated(t)
	link := loopbackLink(t)
	var saved []savedMetric
	for _, family := range []Family{IPv4, IPv6} {
		err := host.changeIPInterface(link.LUID, family, func(row *ipInterface) {
			saved = append(saved, savedMetric{Family: family, Metric: row.Metric, Auto: row.AutoMetric})
		})
		if err != nil {
			t.Fatalf("read the %s interface: %v", family, err)
		}
	}
	guardLoopback(t, loopbackRecovery{Metrics: saved})

	const metric = 76
	if err := link.SetInterfaceMetric(metric); err != nil {
		t.Fatalf("SetInterfaceMetric: %v", err)
	}
	for _, family := range []Family{IPv4, IPv6} {
		err := host.changeIPInterface(link.LUID, family, func(row *ipInterface) {
			if row.Metric != metric || row.AutoMetric {
				t.Errorf("%s: metric %d, automatic %v; want %d, manual", family, row.Metric, row.AutoMetric, metric)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := link.SetInterfaceMetric(0); err != nil {
		t.Fatalf("SetInterfaceMetric(0): %v", err)
	}
	for _, family := range []Family{IPv4, IPv6} {
		err := host.changeIPInterface(link.LUID, family, func(row *ipInterface) {
			if !row.AutoMetric {
				t.Errorf("%s: metric 0 did not give the metric back to the system", family)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// SetMTU goes through the same read-modify-write as the metric. The loopback's
// MTU is not a number to play with, so the call sets the value it already has;
// a refusal is reported, not failed on, because it says something about the
// loopback and not about an adapter.
func TestRootSetMTUWritesBackTheCurrentValue(t *testing.T) {
	requireElevated(t)
	link := loopbackLink(t)
	var current uint32
	if err := host.changeIPInterface(link.LUID, IPv4, func(row *ipInterface) { current = row.MTU }); err != nil {
		t.Fatal(err)
	}
	if err := link.SetMTU(current, IPv4); err != nil {
		t.Logf("DIAGNOSTIC: SetMTU(%d, IPv4) on the loopback: %v", current, err)
	}
}
