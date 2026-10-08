package ovpn

import (
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/fsperm"
)

const (
	windowsWorkspace = `C:\ProgramData\Plaitway\run\ovpn-0123456789ab`
	windowsAdapter   = "Plaitway-ovpn-0123abcd"
)

func windowsArgs(bin binaryInfo) []string {
	return buildArgs(bin, windowsWorkspace+`\profile.ovpn`, newTCPChannel(windowsWorkspace, nil).options(), []string{"--dev-node", windowsAdapter})
}

// The golden text of the command line the Windows engine gives openvpn.
func TestBuildArgsWindowsGolden(t *testing.T) {
	got := strings.Join(windowsArgs(binaryInfo{available: true, major: 2, minor: 7}), "\n") + "\n"
	checkGolden(t, "args-windows-2.7.golden", []byte(got))
}

// What keeps openvpn from touching the machine's network on Windows. Each of
// these is a decision that was found out against openvpn 2.7.1 and written
// down in platform_windows.go.
func TestWindowsCommandLineLeavesTheNetworkToTheEngine(t *testing.T) {
	args := windowsArgs(binaryInfo{available: true, major: 2, minor: 7})
	has := func(sequence ...string) bool {
		for i := 0; i+len(sequence) <= len(args); i++ {
			if slices.Equal(args[i:i+len(sequence)], sequence) {
				return true
			}
		}
		return false
	}
	for _, sequence := range [][]string{
		{"--route-noexec"},
		{"--ifconfig-noexec"},
		{"--ip-win32", "manual"},
		{"--dns-updown", "disable"},
		{"--disable-dco"},
		{"--script-security", "1"},
		{"--dev-node", windowsAdapter},
		{"--management", "127.0.0.1", "0"},
	} {
		if !has(sequence...) {
			t.Errorf("command line lacks %v: %v", sequence, args)
		}
	}
	// A server may push these, and the profile may filter or accept them: the
	// filters of the daemon come first, and the first match decides.
	configAt := slices.Index(args, "--config")
	for _, text := range []string{"block-outside-dns", "ip-win32", "dns ", "dhcp-option"} {
		at := -1
		for i := 0; i+2 < len(args); i++ {
			if args[i] == "--pull-filter" && args[i+1] == "ignore" && args[i+2] == text {
				at = i
			}
		}
		if at < 0 || at > configAt {
			t.Errorf("--pull-filter ignore %s is at %d, --config at %d: it must come before the profile", text, at, configAt)
		}
	}
	for _, forbidden := range []string{
		"--service", "--msg-channel", "--register-dns", "--block-outside-dns", "--dhcp-renew", "--dhcp-release",
		"--daemon", "--log", "--log-append", "--windows-driver", "unix",
	} {
		if slices.Contains(args, forbidden) {
			t.Errorf("command line has %s: %v", forbidden, args)
		}
	}
	for _, a := range args {
		if strings.Contains(strings.ToLower(a), "password") && !strings.HasSuffix(a, "mgmt.pw") && a != "--management-query-passwords" {
			t.Errorf("argument %q looks like it carries a credential", a)
		}
	}
}

func TestWindowsTunnelDeviceNames(t *testing.T) {
	for _, name := range []string{windowsAdapter, "Plaitway-test-ovpn-0123abcd", "utun11", "null", "tun0"} {
		if !validTunnelDevice(name) {
			t.Errorf("%q was refused", name)
		}
	}
	for _, name := range []string{"", `..\x`, "Plaitway-ovpn-", "Plaitway-ovpn-0123abcg", "a b", "tap/0", "Plaitway-ovpn-0123abcd0"} {
		if validTunnelDevice(name) {
			t.Errorf("%q was accepted", name)
		}
	}
}

// The profile and the management password are in the engine's directory, and
// only the daemon's accounts may read it.
func TestPrepareWorkspaceIsPrivateOnWindows(t *testing.T) {
	runDir := shortTempDir(t) + `\run`
	e := newTestEngine(t, runDir, minimalProfile)
	if err := e.prepareWorkspace(); err != nil {
		t.Fatal(err)
	}
	defer e.removeWorkspace()

	for _, path := range []string{runDir, e.dir, e.configPath, e.dir + `\` + passwordFileName} {
		private, err := fsperm.IsPrivate(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !private {
			t.Errorf("%s can be read by accounts the daemon does not trust", path)
		}
	}
	if _, err := os.Stat(e.dir + `\` + passwordFileName); err != nil {
		t.Errorf("the management password was not written: %v", err)
	}
}

// A profile that opens a device gets an adapter of the engine's making; one that
// opens none (the tests', "dev null") needs none.
func TestWindowsDeviceProviderFollowsTheProfile(t *testing.T) {
	cfg := Config{Binary: `C:\Program Files\OpenVPN\bin\openvpn.exe`}
	tests := []struct {
		device      string
		wantAdapter bool
	}{
		{"tun", true}, {"tun0", true}, {"tap", true}, {"", true}, {"null", false},
	}
	for _, tt := range tests {
		prof := &profile{device: tt.device}
		provider := newDeviceProvider(cfg, "office", prof, slog.New(slog.NewTextHandler(io.Discard, nil)))
		adapter, isAdapter := provider.(*adapterDevice)
		if isAdapter != tt.wantAdapter {
			t.Errorf("dev %q: provider %T", tt.device, provider)
		}
		if isAdapter && (adapter.prefix != ownAdapterPrefix || adapter.name() != adapterName(ownAdapterPrefix, "office")) {
			t.Errorf("dev %q: adapter %q with prefix %q", tt.device, adapter.name(), adapter.prefix)
		}
	}
	if tool, ok := newDeviceProvider(cfg, "o", &profile{device: "tun"}, slog.New(slog.NewTextHandler(io.Discard, nil))).(*adapterDevice).tool.(tapctl); !ok || tool.path != `C:\Program Files\OpenVPN\bin\tapctl.exe` {
		t.Errorf("tapctl = %+v: it must be the one beside openvpn", tool)
	}
}
