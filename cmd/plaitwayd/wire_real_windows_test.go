package main

import (
	"context"
	"log/slog"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// missingTunnel is an interface the machine does not have, with a name as long
// as the adapters of the OpenVPN engine ("Plaitway-ovpn-" and 8 hex digits are
// 22 characters, more than the 15 of a macOS interface).
const missingTunnel = "Plaitway-test-keying-no-such-adapter"

// The Windows daemon must tell the Reconciler that the routing table is keyed by
// interface and next hop: the zero value is the macOS table, whose interface
// names stop at 15 characters and whose routes carry no interface index. The
// intent names an interface that does not exist, so that the Reconciler waits
// for it and writes no route to this machine.
func TestWireRealKeysTheRoutingTableTheWindowsWay(t *testing.T) {
	skipIfElevated(t)
	dir := shortDir(t)
	_, rec, _, err := wireReal(realConfig{log: discardLog(), runDir: filepath.Join(dir, "run"), stateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- rec.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-ended:
			if err != nil {
				t.Errorf("the Reconciler ended with %v", err)
			}
		case <-time.After(waitTimeout):
			t.Error("the Reconciler did not end")
		}
	}()

	err = rec.Announce(tunnel.Intent{
		Owner:    "keying-test",
		State:    tunnel.StateUp,
		Iface:    missingTunnel,
		Priority: 1,
		UpSince:  time.Now(),
		Routes:   []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}, // TEST-NET-2
	})
	if err != nil {
		t.Fatalf("Announce: %v; an interface name of 36 characters is valid on Windows", err)
	}
	routes := rec.Report().Routes
	if len(routes) != 1 || !strings.Contains(routes[0].Detail, "not found") {
		t.Fatalf("report %+v, want the route to wait for an interface the machine does not have", routes)
	}
}

// The state directory is made private before the Reconciler puts its journal in
// it: reconciler.New would create it with the access list it inherits. Here the
// parent lets Everyone read, as ProgramData does for a new folder.
func TestWireRealMakesTheStateDirectoryPrivateBeforeTheJournalExists(t *testing.T) {
	skipIfElevated(t)
	dir := shortDir(t)
	if out, err := exec.Command("icacls", dir, "/grant", everyoneSID+":(OI)(CI)R").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v\n%s", err, out)
	}
	stateDir := filepath.Join(dir, "state")
	_, rec, _, err := wireReal(realConfig{log: discardLog(), runDir: filepath.Join(dir, "run"), stateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- rec.Run(ctx) }()
	cancel()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}

	mustBePrivate(t, stateDir)
	mustBePrivate(t, filepath.Join(stateDir, journalFileName))
}

// An engine that cannot run is in the log with its reason, so that the log of a
// service nobody watches says why a profile cannot connect.
func TestWireRealLogsWhyAnEngineIsUnavailable(t *testing.T) {
	skipIfElevated(t)
	dir := shortDir(t)
	var logged syncBuffer
	log, _ := newLogger(&logged, slog.LevelInfo)
	missing := filepath.Join(dir, "missing", openvpnFileName)
	_, rec, _, err := wireReal(realConfig{log: log, openvpn: missing, runDir: filepath.Join(dir, "run"), stateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- rec.Run(ctx) }()
	cancel()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"engine unavailable", "engine=openvpn", "openvpn not found at",
		"engine=wireguard", wintunReasonPart,
	} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logged.String())
		}
	}
}
