//go:build rootintegration && linux

package linux

// These tests run the adapter for real against a real systemd-resolved and
// resolvectl. They start their own resolved on a private D-Bus, inside a
// private user, network and mount namespace, so that the host's resolver, links
// and /run are not touched: build the test binary and run it as root of a
// namespace of your own.
//
//	go test -c -tags rootintegration -o dns.test ./internal/osnet/linux
//	unshare -Urnm sh -c 'PLAITWAY_ROOT_TESTS=1 ./dns.test -test.v -test.run ResolvedEndToEnd'
//
// They refuse to run in the initial user namespace, and are skipped when
// dbus-daemon, systemd-resolved, resolvectl or ip is missing. In the namespace
// /run is a tmpfs of its own and /etc/passwd and /etc/group are replaced by
// copies in which systemd-resolve is root, because resolved drops its
// privileges to that user and a namespace with one user cannot do that.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

const (
	testBusPath       = "/run/plaitway-test-bus.sock"
	testBusConfigPath = "/run/plaitway-test-bus.conf"
	testBusConfig     = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>system</type>
  <listen>unix:path=` + testBusPath + `</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow user="*"/>
    <allow own="*"/>
    <allow send_type="method_call"/>
    <allow send_type="signal"/>
    <allow send_type="method_return"/>
    <allow send_type="error"/>
    <allow receive_type="method_call"/>
    <allow receive_type="signal"/>
    <allow receive_type="method_return"/>
    <allow receive_type="error"/>
  </policy>
</busconfig>
`
)

var (
	namespaceOnce sync.Once
	namespaceErr  error
)

func requirePrivateNamespace(t *testing.T) {
	t.Helper()
	if os.Getenv("PLAITWAY_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Fatal("refusing to run: set PLAITWAY_ROOT_TESTS=1 and run it as root of a namespace made by unshare -Urnm")
	}
	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if f := strings.Fields(string(uidMap)); err != nil || len(f) == 3 && f[2] == "4294967295" {
		t.Fatalf("refusing to run in the initial user namespace (uid_map %q, err %v)", uidMap, err)
	}
}

// resolvedStack is a running resolved and the runner that reaches it.
type resolvedStack struct {
	run      CommandRunner
	resolved *exec.Cmd
	// links are the interfaces the test made, removed when it ends: the other root
	// tests of the package refuse a namespace that has anything besides lo.
	links []string
}

// startResolved prepares the namespace and starts dbus-daemon and
// systemd-resolved, which are killed again when the test ends.
func startResolved(t *testing.T) *resolvedStack {
	t.Helper()
	requirePrivateNamespace(t)
	dbus, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon")
	}
	resolvedPath := ""
	for _, p := range []string{"/usr/lib/systemd/systemd-resolved", "/lib/systemd/systemd-resolved"} {
		if _, err := os.Stat(p); err == nil {
			resolvedPath = p
			break
		}
	}
	if _, err := os.Stat(resolvectlPath); err != nil || resolvedPath == "" {
		t.Skip("no systemd-resolved or resolvectl")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("no ip")
	}

	namespaceOnce.Do(func() { namespaceErr = prepareNamespace(t.TempDir()) })
	if namespaceErr != nil {
		t.Fatalf("preparing the namespace (run under unshare -Urnm): %v", namespaceErr)
	}

	if err := os.WriteFile(testBusConfigPath, []byte(testBusConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(testBusPath)
	startDaemon(t, exec.Command(dbus, "--config-file="+testBusConfigPath, "--nofork", "--nopidfile"))

	s := &resolvedStack{run: func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin", "DBUS_SYSTEM_BUS_ADDRESS=unix:path=" + testBusPath}
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.CombinedOutput()
	}}
	waitFor(t, "the bus", func() bool { _, err := os.Stat(testBusPath); return err == nil })

	s.resolved = exec.Command(resolvedPath)
	s.resolved.Env = append(os.Environ(), "DBUS_SYSTEM_BUS_ADDRESS=unix:path="+testBusPath)
	startDaemon(t, s.resolved)
	waitFor(t, "systemd-resolved", func() bool { _, err := s.resolvectl("dns"); return err == nil })
	t.Cleanup(func() {
		for _, name := range s.links {
			_ = exec.Command("ip", "link", "del", name).Run()
		}
	})
	return s
}

// prepareNamespace gives the namespace a /run and a passwd of its own.
func prepareNamespace(dir string) error {
	if err := unix.Mount("none", "/run", "tmpfs", 0, ""); err != nil {
		return err
	}
	if err := os.MkdirAll("/run/systemd", 0o755); err != nil {
		return err
	}
	for _, file := range []string{"/etc/passwd", "/etc/group"} {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var lines []string
		for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
			if !strings.HasPrefix(line, "systemd-resolve:") {
				lines = append(lines, line)
			}
		}
		lines = append(lines, "systemd-resolve:x:0:0::/:/usr/sbin/nologin")
		if file == "/etc/group" {
			lines[len(lines)-1] = "systemd-resolve:x:0:"
		}
		replacement := filepath.Join(dir, filepath.Base(file))
		if err := os.WriteFile(replacement, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return err
		}
		if err := unix.Mount(replacement, file, "", unix.MS_BIND, ""); err != nil {
			return err
		}
	}
	out, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput()
	if err != nil {
		return errors.New("ip link set lo up: " + string(out))
	}
	return nil
}

func startDaemon(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	var log strings.Builder
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() && log.Len() > 0 {
			t.Logf("%s said:\n%s", filepath.Base(cmd.Path), log.String())
		}
	})
}

func waitFor(t *testing.T, what string, ready func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ready() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (s *resolvedStack) resolvectl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.run(ctx, resolvectlPath, args, "")
	return string(out), err
}

// addLink makes a dummy interface and waits until resolved has heard of it. An
// interface of an earlier run in the same namespace is replaced.
func (s *resolvedStack) addLink(t *testing.T, name string) {
	t.Helper()
	_ = exec.Command("ip", "link", "del", name).Run()
	s.links = append(s.links, name)
	for _, args := range [][]string{{"link", "add", name, "type", "dummy"}, {"link", "set", name, "up"}} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	waitFor(t, "resolved to know "+name, func() bool { _, err := s.resolvectl("dns", "--", name); return err == nil })
}

func (s *resolvedStack) delLink(t *testing.T, name string) {
	t.Helper()
	if out, err := exec.Command("ip", "link", "del", name).CombinedOutput(); err != nil {
		t.Fatalf("ip link del %s: %v: %s", name, err, out)
	}
}

// show returns the words resolvectl prints for one setting of a link, read
// independently of the adapter.
func (s *resolvedStack) show(t *testing.T, verb, iface string) []string {
	t.Helper()
	out, err := s.resolvectl(verb, "--", iface)
	if err != nil {
		t.Fatalf("resolvectl %s %s: %v: %s", verb, iface, err, out)
	}
	_, words, _ := strings.Cut(strings.TrimSpace(out), "):")
	return strings.Fields(words)
}

func (s *resolvedStack) expect(t *testing.T, iface string, servers, domains []string, route string) {
	t.Helper()
	if got := s.show(t, "dns", iface); !slices.Equal(got, servers) {
		t.Errorf("%s: servers %q, want %q", iface, got, servers)
	}
	if got := s.show(t, "domain", iface); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(domains))) {
		t.Errorf("%s: domains %q, want %q (in any order)", iface, got, domains)
	}
	if route != "" {
		if got := s.show(t, "default-route", iface); !slices.Equal(got, []string{route}) {
			t.Errorf("%s: default route %q, want %q", iface, got, route)
		}
	}
}

func TestResolvedEndToEnd(t *testing.T) {
	s := startResolved(t)
	d := NewDNS(DNSOptions{Run: s.run, Logger: dnsTestLog()})
	owned := func(t *testing.T, want ...string) {
		t.Helper()
		got, err := d.Owned()
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("Owned = %q, %v, want %q", got, err, want)
		}
	}

	t.Run("catch-all", func(t *testing.T) {
		s.addLink(t, "tun0")
		if err := d.Apply("office", []osnet.DNSEntry{{Servers: dnsAddrs("10.6.0.1", "fd00::1"), MatchDomains: []string{"."}, Iface: "tun0"}}); err != nil {
			t.Fatal(err)
		}
		s.expect(t, "tun0", []string{"10.6.0.1", "fd00::1"}, []string{"~."}, "yes")
		owned(t, "plaitway:office:tun0")
		if err := d.Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		if err := d.Remove("office"); err != nil {
			t.Fatal(err)
		}
		s.expect(t, "tun0", nil, nil, "")
		owned(t)
	})

	t.Run("split DNS with entries to merge", func(t *testing.T) {
		s.addLink(t, "tun1")
		err := d.Apply("lab", []osnet.DNSEntry{
			{Servers: dnsAddrs("10.7.0.53", "::ffff:10.7.0.54"), MatchDomains: []string{"Corp.Example.COM.", "lan"}, Order: 20, Iface: "tun1"},
			{Servers: dnsAddrs("10.7.0.52", "10.7.0.53"), MatchDomains: []string{"_srv.example", "lan"}, Order: 10, Iface: "tun1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		s.expect(t, "tun1",
			[]string{"10.7.0.52", "10.7.0.53", "10.7.0.54"},
			[]string{"~_srv.example", "~lan", "~corp.example.com"}, "no")
		// Read the way an administrator would.
		status, err := s.resolvectl("status", "tun1")
		if err != nil || !strings.Contains(status, "Default Route: no") || !strings.Contains(status, "~corp.example.com") {
			t.Errorf("status = %q, %v", status, err)
		}
		owned(t, "plaitway:lab:tun1")
	})

	t.Run("replace and move", func(t *testing.T) {
		s.addLink(t, "tun2")
		s.addLink(t, "tun3")
		for _, iface := range []string{"tun2", "tun2", "tun3"} {
			if err := d.Apply("home", []osnet.DNSEntry{{Servers: dnsAddrs("10.8.0.1"), MatchDomains: []string{"."}, Iface: iface}}); err != nil {
				t.Fatal(err)
			}
		}
		s.expect(t, "tun2", nil, nil, "")
		s.expect(t, "tun3", []string{"10.8.0.1"}, []string{"~."}, "yes")
		owned(t, "plaitway:home:tun3", "plaitway:lab:tun1")
		if err := d.Apply("home", []osnet.DNSEntry{{Servers: dnsAddrs("10.8.0.2"), MatchDomains: []string{"home.example"}, Iface: "tun3"}}); err != nil {
			t.Fatal(err)
		}
		s.expect(t, "tun3", []string{"10.8.0.2"}, []string{"~home.example"}, "no")
	})

	t.Run("sweep with the keys of Owned", func(t *testing.T) {
		keys, err := d.Owned()
		if err != nil || len(keys) != 2 {
			t.Fatalf("Owned = %q, %v", keys, err)
		}
		for _, key := range keys {
			if err := d.Remove(key); err != nil {
				t.Fatal(err)
			}
		}
		owned(t)
		s.expect(t, "tun1", nil, nil, "")
		s.expect(t, "tun3", nil, nil, "")
	})

	t.Run("a link that is gone", func(t *testing.T) {
		s.addLink(t, "tun4")
		s.addLink(t, "tun5")
		entry := func(iface string) []osnet.DNSEntry {
			return []osnet.DNSEntry{{Servers: dnsAddrs("10.9.0.1"), MatchDomains: []string{"."}, Iface: iface}}
		}
		if err := d.Apply("gone", entry("tun4")); err != nil {
			t.Fatal(err)
		}
		s.delLink(t, "tun4")
		owned(t)
		if err := d.Remove("gone"); err != nil {
			t.Errorf("Remove: %v", err)
		}
		// Moving away from a link that has gone.
		s.addLink(t, "tun6")
		if err := d.Apply("gone", entry("tun5")); err != nil {
			t.Fatal(err)
		}
		s.delLink(t, "tun5")
		if err := d.Apply("gone", entry("tun6")); err != nil {
			t.Fatalf("Apply after the old link went: %v", err)
		}
		if err := d.Remove("gone"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a link somebody else configured", func(t *testing.T) {
		s.addLink(t, "tun7")
		if err := d.Apply("mine", []osnet.DNSEntry{{Servers: dnsAddrs("10.10.0.1"), MatchDomains: []string{"."}, Iface: "tun7"}}); err != nil {
			t.Fatal(err)
		}
		if out, err := s.resolvectl("dns", "--", "tun7", "9.9.9.9"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		owned(t)
		if err := d.Remove("mine"); err != nil {
			t.Fatal(err)
		}
		if got := s.show(t, "dns", "tun7"); !slices.Equal(got, []string{"9.9.9.9"}) {
			t.Errorf("their servers are %q, want 9.9.9.9", got)
		}
	})

	// resolvectl reads a number as an interface index: the link called 99 cannot
	// be reached by its name, which is why such a name is refused.
	t.Run("an interface named like an index", func(t *testing.T) {
		_ = exec.Command("ip", "link", "del", "99").Run()
		s.links = append(s.links, "99")
		if out, err := exec.Command("ip", "link", "add", "99", "type", "dummy").CombinedOutput(); err != nil {
			t.Fatalf("ip link add 99: %v: %s", err, out)
		}
		time.Sleep(200 * time.Millisecond)
		out, err := s.resolvectl("dns", "--", "99")
		if err == nil || !strings.Contains(out, "No such device") {
			t.Errorf("resolvectl dns -- 99 = %q, %v: expected it to look for the index", out, err)
		}
		err = d.Apply("number", []osnet.DNSEntry{{Servers: dnsAddrs("10.11.0.1"), MatchDomains: []string{"."}, Iface: "99"}})
		if err == nil {
			t.Error("Apply accepted an interface named 99")
		}
	})

	t.Run("resolved is not running", func(t *testing.T) {
		s.addLink(t, "tun9")
		if err := d.Apply("last", []osnet.DNSEntry{{Servers: dnsAddrs("10.12.0.1"), MatchDomains: []string{"."}, Iface: "tun9"}}); err != nil {
			t.Fatal(err)
		}
		_ = s.resolved.Process.Kill()
		_ = s.resolved.Wait()

		err := d.Apply("late", []osnet.DNSEntry{{Servers: dnsAddrs("10.12.0.2"), MatchDomains: []string{"."}, Iface: "tun9"}})
		if !errors.Is(err, errNoResolved) || !strings.Contains(err.Error(), "systemd-resolved is not running") {
			t.Errorf("Apply = %v, want systemd-resolved is not running", err)
		} else {
			t.Logf("Apply says: %v", err)
		}
		if err := d.Flush(); !errors.Is(err, errNoResolved) {
			t.Errorf("Flush = %v, want systemd-resolved is not running", err)
		}
		owned(t)
		if err := d.Remove("last"); err != nil {
			t.Errorf("Remove: %v", err)
		}
	})
}
