//go:build rootintegration

package main

// WARNING: this test starts the real daemon, with its real engines, inside an
// elevated process. The daemon removes every DNS rule that carries the
// Plaitway marker when it starts, so the test refuses to run while the
// PlaitwayHelper service runs or any such rule exists. It connects no profile
// and creates no adapter; it changes no route, DNS rule or adapter, and checks
// that. OpenVPN is left unconfigured, so that not even openvpn.exe is started.
//
// From an elevated shell:
//
//	go test -count=1 -tags rootintegration -run TestRootRealDaemon -v ./cmd/plaitwayd

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"golang.org/x/sys/windows/svc"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	winnet "github.com/KoukeNeko/Plaitway/internal/osnet/windows"
)

// routeKeys names the routes of the machine by what identifies them, so that
// two reads can be compared.
func routeKeys(t *testing.T) []string {
	t.Helper()
	routes, err := winnet.NewRouteTable().Dump()
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(routes))
	for _, route := range routes {
		keys = append(keys, fmt.Sprintf("%s if%d via %s", route.Dst, route.IfIndex, route.Gateway))
	}
	slices.Sort(keys)
	return keys
}

func plaitwayDNSRules(t *testing.T) []string {
	t.Helper()
	rules, err := winnet.NewDNS(winnet.DNSOptions{}).Owned()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(rules)
	return rules
}

func TestRootRealDaemonStartsElevatedAndStopsLeavingTheMachineAsItWas(t *testing.T) {
	if !isPrivileged() {
		t.Skip("run from an elevated shell: the test starts the daemon with its real engines as an administrator")
	}
	if status, err := queryServiceStatus(serviceName); err == nil && status.State != svc.Stopped {
		t.Skipf("the %s service runs: a second daemon would remove its DNS rules", serviceName)
	}
	if rules := plaitwayDNSRules(t); len(rules) > 0 {
		t.Skipf("DNS rules of Plaitway exist already (%v): the daemon would remove them", rules)
	}
	routesBefore := routeKeys(t)

	cfg := realDaemonConfig(t, "")
	client, stop := startRun(t, cfg, everyone().pol)
	info, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
	if err != nil || !info.Privileged {
		t.Fatalf("GetDaemonInfo: %v, %v; want a daemon that says it is privileged", info, err)
	}
	if openvpn := daemonEngine(t, client, pb.ProfileKind_PROFILE_KIND_OPENVPN); openvpn.Available {
		t.Errorf("OpenVPN is available without a binary: %+v", openvpn)
	}
	if err := stop(); err != nil {
		t.Fatalf("run returned %v", err)
	}
	socketGone(t, cfg.socket)

	if after := routeKeys(t); !slices.Equal(after, routesBefore) {
		t.Errorf("the routing table changed: %d routes before, %d after", len(routesBefore), len(after))
	}
	if rules := plaitwayDNSRules(t); len(rules) > 0 {
		t.Errorf("DNS rules of Plaitway are left: %v", rules)
	}
}
