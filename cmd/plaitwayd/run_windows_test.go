package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
	"github.com/KoukeNeko/Plaitway/internal/manager"
)

// A daemon on the real engines reads the routing table, the DNS rules and the
// interfaces when it starts, and an elevated one also removes the Plaitway DNS
// rules it finds: those of the PlaitwayHelper service of this machine, if it
// runs. So these tests run unelevated only, where the daemon can read but not
// write. A test with the real wiring never connects a profile.
func skipIfElevated(t *testing.T) {
	t.Helper()
	if isPrivileged() {
		t.Skip("the real engines would manage the routes and DNS rules of this machine; run unelevated")
	}
}

const (
	// A private key and a public key that parse, from the WireGuard
	// documentation. No tunnel is ever started with them.
	exampleWireGuardProfile = "[Interface]\nPrivateKey = yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=\nAddress = 10.6.0.2/32\n" +
		"[Peer]\nPublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n"
	// untrustedOpenVPNReason starts what the engine says about a binary it
	// refuses; wintunReasonPart is what it says without a wintun.dll.
	untrustedOpenVPNReason = "openvpn is not trusted"
	wintunReasonPart       = "wintun.dll"
)

// realDaemonConfig is the daemon on the real engines, with its pipe, state and
// run directories in a scratch space. A path of "" for openvpn leaves the
// engine without a binary.
func realDaemonConfig(t *testing.T, openvpn string) config {
	t.Helper()
	dir := shortDir(t)
	return config{socket: newSocketPath(t), stateDir: filepath.Join(dir, "state"), runDir: filepath.Join(dir, "run"), openvpn: openvpn}
}

func daemonEngine(t *testing.T, client pb.DaemonServiceClient, kind pb.ProfileKind) *pb.EngineInfo {
	t.Helper()
	info, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
	if err != nil {
		t.Fatalf("GetDaemonInfo: %v", err)
	}
	for _, engine := range info.Engines {
		if engine.Kind == kind {
			return engine
		}
	}
	t.Fatalf("DaemonInfo lists no engine of kind %v: %v", kind, info.Engines)
	return nil
}

// writableOpenVPN is a file where the user can write, which the engine must not
// run as LocalSystem.
func writableOpenVPN(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), openvpnFileName)
	if err := os.WriteFile(path, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWithTheRealEnginesStartsAndSaysWhyEachEngineIsUnavailable(t *testing.T) {
	skipIfElevated(t)
	missing := filepath.Join(t.TempDir(), "missing", openvpnFileName)
	writable := writableOpenVPN(t)
	tests := []struct {
		name    string
		openvpn string
		want    []string
	}{
		{"in a folder the user can write to", writable, []string{untrustedOpenVPNReason, writable}},
		{"not there", missing, []string{untrustedOpenVPNReason, "openvpn not found at " + missing}},
		{"a bare name, which would be looked up in any folder", openvpnFileName, []string{untrustedOpenVPNReason, "not an absolute path"}},
		{"not configured", "", []string{untrustedOpenVPNReason, "not configured"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := realDaemonConfig(t, tt.openvpn)
			client, stop := startRun(t, cfg, everyone().pol)

			info, err := client.GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{})
			if err != nil || info.Privileged {
				t.Fatalf("GetDaemonInfo: %v, %v; want an answer that says the daemon is not privileged", info, err)
			}
			openvpn := daemonEngine(t, client, pb.ProfileKind_PROFILE_KIND_OPENVPN)
			if openvpn.Available {
				t.Errorf("OpenVPN is available: %+v", openvpn)
			}
			for _, part := range tt.want {
				if !strings.Contains(openvpn.Detail, part) {
					t.Errorf("OpenVPN reason %q lacks %q", openvpn.Detail, part)
				}
			}
			wireguard := daemonEngine(t, client, pb.ProfileKind_PROFILE_KIND_WIREGUARD)
			if wireguard.Available || !strings.Contains(wireguard.Detail, wintunReasonPart) {
				t.Errorf("WireGuard: %+v, want it unavailable because of %s", wireguard, wintunReasonPart)
			}

			if err := stop(); err != nil {
				t.Fatalf("run returned %v", err)
			}
			socketGone(t, cfg.socket)
			mustBePrivate(t, cfg.stateDir)
		})
	}
}

// The profile of an engine that cannot run shows the engine's reason as its
// last error, and the daemon neither starts the tunnel nor touches the host.
func TestProfilesOfUnavailableEnginesFailWithTheReason(t *testing.T) {
	skipIfElevated(t)
	cfg := realDaemonConfig(t, writableOpenVPN(t))
	client, _ := startRun(t, cfg, everyone().pol)

	profiles := map[string]string{
		"openvpn":   ovpnProfile,
		"wireguard": exampleWireGuardProfile,
	}
	reasons := map[string]string{"openvpn": untrustedOpenVPNReason, "wireguard": wintunReasonPart}
	for name, content := range profiles {
		imported, err := client.ImportProfile(context.Background(), &pb.ImportProfileRequest{Name: name, Content: []byte(content)})
		if err != nil {
			t.Fatalf("ImportProfile(%s): %v", name, err)
		}
		if _, err := client.SetProfileEnabled(context.Background(), &pb.SetProfileEnabledRequest{Id: imported.Profile.Id, Enabled: true}); err != nil {
			t.Fatalf("SetProfileEnabled(%s): %v", name, err)
		}
		eventually(t, name+" to fail with the reason of its engine", func() bool {
			p := profileByID(t, client, imported.Profile.Id)
			return p.State == pb.ProfileState_PROFILE_STATE_FAILED &&
				strings.Contains(p.LastError, "engine not available") && strings.Contains(p.LastError, reasons[name])
		})
	}
}

func profileByID(t *testing.T, client pb.DaemonServiceClient, id string) *pb.Profile {
	t.Helper()
	list, err := client.ListProfiles(context.Background(), &pb.ListProfilesRequest{})
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	for _, p := range list.Profiles {
		if p.Id == id {
			return p
		}
	}
	t.Fatalf("profile %s is gone", id)
	return nil
}

// A daemon built for one OpenVPN (-X main.openvpnSHA256) holds the engine to
// it, and the engine keeps working with the one it was built for. Run against
// the standard installation, so it skips where there is none that the checks of
// internal/ovpn accept.
func TestTheBuildTimeHashOfOpenVPNReachesTheEngine(t *testing.T) {
	skipIfElevated(t)
	installed := defaultOpenVPN()
	data, err := os.ReadFile(installed)
	if err != nil {
		t.Skipf("no OpenVPN at %s: %v", installed, err)
	}
	sum := sha256.Sum256(data)
	defer func(old string) { openvpnSHA256 = old }(openvpnSHA256)

	openvpnState := func(pinned string) *pb.EngineInfo {
		openvpnSHA256 = pinned
		client, stop := startRun(t, realDaemonConfig(t, installed), everyone().pol)
		defer stop()
		return daemonEngine(t, client, pb.ProfileKind_PROFILE_KIND_OPENVPN)
	}
	if unpinned := openvpnState(""); !unpinned.Available {
		t.Skipf("the OpenVPN at %s is not usable here: %s", installed, unpinned.Detail)
	}

	if pinned := openvpnState(hex.EncodeToString(sum[:])); !pinned.Available {
		t.Errorf("OpenVPN with the hash of the installed binary: %+v, want it available", pinned)
	}
	other := sha256.Sum256([]byte("another build"))
	wrong := openvpnState(hex.EncodeToString(other[:]))
	if wrong.Available || !strings.Contains(wrong.Detail, "is not the binary this daemon was built for") {
		t.Errorf("OpenVPN with the hash of another binary: %+v, want it refused for the hash", wrong)
	}
}

// What stops a daemon that cannot work is said with its cause, and the pipe is
// given back: the service log is the only place the cause is read.
func TestRunWithTheRealEnginesReportsWhyItCannotStart(t *testing.T) {
	skipIfElevated(t)
	tests := []struct {
		name    string
		prepare func(t *testing.T, cfg config)
		// want is what the error must name.
		want func(cfg config) []string
	}{
		{
			name: "the state directory is a file",
			prepare: func(t *testing.T, cfg config) {
				if err := os.MkdirAll(filepath.Dir(cfg.stateDir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfg.stateDir, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: func(cfg config) []string { return []string{"state directory", cfg.stateDir} },
		},
		{
			name: "the journal is a directory",
			prepare: func(t *testing.T, cfg config) {
				if err := os.MkdirAll(filepath.Join(cfg.stateDir, journalFileName), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: func(config) []string { return []string{"start the Reconciler", journalFileName} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := realDaemonConfig(t, writableOpenVPN(t))
			tt.prepare(t, cfg)

			err := run(context.Background(), discardLog(), manager.NewLogBuffer(""), cfg, everyone().pol)

			if err == nil {
				t.Fatal("the daemon started")
			}
			for _, part := range tt.want(cfg) {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q lacks %q", err, part)
				}
			}
			socketGone(t, cfg.socket)
		})
	}
}
