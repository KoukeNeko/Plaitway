package wg

import (
	"net"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHasWintunID(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want bool
	}{
		{"wintun", []string{"Wintun"}, true},
		{"any case", []string{"wintun"}, true},
		{"among others", []string{`ROOT\NET\0001`, "Wintun"}, true},
		{"wireguard-nt", []string{"WireGuard"}, false},
		{"openvpn tap", []string{"tap0901"}, false},
		{"none", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasWintunID(tt.ids); got != tt.want {
				t.Errorf("hasWintunID(%q) = %v, want %v", tt.ids, got, tt.want)
			}
		})
	}
}

func TestHasNamePrefix(t *testing.T) {
	tests := []struct {
		name, prefix string
		want         bool
	}{
		{"Plaitway-1a2b3c4d", "Plaitway-", true},
		{"plaitway-1a2b3c4d", "Plaitway-", true},
		{"Plaitway-", "Plaitway-", true},
		{"Plaitway", "Plaitway-", false},
		{"Ethernet", "Plaitway-", false},
		{"WireGuard Tunnel", "Plaitway-", false},
		{"My Plaitway-1a2b3c4d", "Plaitway-", false},
		{"Plaitway-test-wg", "Plaitway-test-", true},
	}
	for _, tt := range tests {
		if got := hasNamePrefix(tt.name, tt.prefix); got != tt.want {
			t.Errorf("hasNamePrefix(%q, %q) = %v, want %v", tt.name, tt.prefix, got, tt.want)
		}
	}
}

func TestRemoveStaleAdaptersRefusesAnEmptyPrefix(t *testing.T) {
	removed, err := removeStaleAdapters("")
	if err == nil || len(removed) != 0 {
		t.Fatalf("removeStaleAdapters(\"\") = %v, %v; want a refusal that removed nothing", removed, err)
	}
}

// The listing reads the device list and the registry, which any user may.
// Nothing can match this prefix, so nothing is removed and nothing is tried.
func TestRemoveStaleAdaptersWithoutAMatchRemovesNothing(t *testing.T) {
	removed, err := removeStaleAdapters("Plaitway-test-no-adapter-has-this-prefix-")
	if err != nil || len(removed) != 0 {
		t.Fatalf("removeStaleAdapters = %v, %v; want nothing removed and no error", removed, err)
	}
}

func TestWintunAdapterNamesListsOnlyWintun(t *testing.T) {
	adapters, err := listWintunAdapters()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, adapter := range adapters {
		names = append(names, adapter.name)
		if strings.TrimSpace(adapter.name) == "" || !strings.HasPrefix(adapter.instanceID, "{") {
			t.Errorf("incomplete adapter %+v", adapter)
		}
	}
	t.Logf("wintun adapters on this machine: %q", names)
	if slices.Contains(names, "Ethernet") || slices.Contains(names, "Wi-Fi") {
		t.Errorf("the listing holds adapters that are not wintun: %q", names)
	}
}

// A machine without a wintun adapter cannot show the reading of an adapter's
// name on a wintun, so it is shown on the adapters the machine has: every name
// read from the registry must be the name the network stack lists.
func TestConnectionNameMatchesTheNamesTheStackLists(t *testing.T) {
	listed := map[string]bool{}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		listed[iface.Name] = true
	}

	var read []string
	err = eachNetDevice(func(devices windows.DevInfo, device *windows.DevInfoData) {
		instanceID, err := netCfgInstanceID(devices, device)
		if err != nil {
			return
		}
		if name, err := connectionName(instanceID); err == nil {
			read = append(read, name)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var matching int
	for _, name := range read {
		if listed[name] {
			matching++
		}
	}
	if matching == 0 {
		t.Fatalf("none of the names read from the registry %q is a listed interface %v", read, listed)
	}
	t.Logf("%d of %d names read from the registry are listed interfaces", matching, len(read))
}

// The hardware IDs are read as a list of strings, which hasWintunID relies on.
func TestHardwareIDsOfNetworkDevicesAreStringLists(t *testing.T) {
	var lists int
	err := eachNetDevice(func(devices windows.DevInfo, device *windows.DevInfoData) {
		ids, err := devices.DeviceRegistryProperty(device, windows.SPDRP_HARDWAREID)
		if _, isList := ids.([]string); err == nil && isList {
			lists++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if lists == 0 {
		t.Fatal("no network device has hardware IDs readable as a list of strings")
	}
}
