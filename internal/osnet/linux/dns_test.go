package linux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// What resolvectl printed on the machine these fixtures come from (systemd 259),
// with the interface names and the addresses replaced.
const (
	// "resolvectl dns": the Global section, then the links. It lists every link
	// of the kernel and prints a line on stderr for each one that
	// systemd-resolved does not know, and fails then; the unknown links here are
	// dummy interfaces made in a private network namespace.
	printedDNSAll = `Global:
Link 2 (eth0): 192.0.2.1
Link 3 (wg0): 198.51.100.1 198.51.100.2 2001:db8::1 2001:db8::2
`
	printedDNSAllWithUnknown = `Failed to get link data for 4: Unknown object '/org/freedesktop/resolve1/link/_34'.
Failed to get link data for 5: Unknown object '/org/freedesktop/resolve1/link/_35'.
Global:
Link 2 (eth0): 192.0.2.1
Link 3 (wg0): 198.51.100.1 198.51.100.2 2001:db8::1 2001:db8::2
`
	// "resolvectl dns|domain|default-route IFACE".
	printedServers      = "Link 3 (wg0): 198.51.100.1 198.51.100.2 2001:db8::1 2001:db8::2\n"
	printedNoDomains    = "Link 2 (eth0):\n"
	printedCatchAll     = "Link 3 (wg0): ~.\n"
	printedDefaultRoute = "Link 3 (wg0): yes\n"
	// Failures of a read.
	printedNoSuchDevice  = "Failed to resolve interface \"nosuch\": No such device\n"
	printedUnknownObject = "Failed to get link data for 8: Unknown object '/org/freedesktop/resolve1/link/_38'.\n"
	// What resolvectl prints without a system bus, and on a bus without
	// systemd-resolved on it (captured on a private bus). Neither has a Global
	// section.
	printedNoBus      = "sd_bus_open_system: No such file or directory\n"
	printedNoResolved = "Failed to get global data: The name org.freedesktop.resolve1 was not provided by any .service files\n"
)

func dnsTestLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func dnsAddrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = netip.MustParseAddr(a)
	}
	return out
}

// fakeResolvedLink is what systemd-resolved holds for one interface.
type fakeResolvedLink struct {
	index   int
	servers []string
	domains []string
	// route is "yes", "no" or "" when nothing was set.
	route string
	// known is false for a link that the kernel has and resolved has not heard of.
	known bool
}

// fakeResolved plays the kernel's interfaces, systemd-resolved and resolvectl:
// the verbs the adapter uses, with the argument handling of getopt and the
// output of the real tool.
type fakeResolved struct {
	mu    sync.Mutex
	links map[string]*fakeResolvedLink
	next  int
	// down: resolved is not running, there is no bus to talk to.
	down bool
	// programs are the paths that exist.
	programs map[string]bool
	runs     []string // "path arg arg ..."
	// failWrite makes a setter verb fail with that output; ignoreWrite makes it
	// do nothing and succeed.
	failWrite   map[string]string
	ignoreWrite map[string]bool
	failRevert  string
}

func newFakeResolved(links ...string) *fakeResolved {
	f := &fakeResolved{
		links:       make(map[string]*fakeResolvedLink),
		next:        2,
		programs:    map[string]bool{resolvectlPath: true, resolvectlAltPath: true},
		failWrite:   make(map[string]string),
		ignoreWrite: make(map[string]bool),
	}
	for _, name := range links {
		f.addLink(name)
	}
	return f
}

func (f *fakeResolved) addLink(name string) *fakeResolvedLink {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := &fakeResolvedLink{index: f.next, known: true}
	f.next++
	f.links[name] = l
	return l
}

func (f *fakeResolved) vanish(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.links, name)
}

// config is what resolved holds for name, as resolvectl reports it.
func (f *fakeResolved) config(name string) linkConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.links[name]
	return linkConfig{Servers: slices.Clone(l.servers), Domains: slices.Clone(l.domains), DefaultRoute: l.defaultRoute()}
}

func (f *fakeResolved) isConfigured(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.links[name]
	return len(l.servers) > 0 || len(l.domains) > 0 || l.route != ""
}

func (f *fakeResolved) setServers(name string, servers ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[name].servers = servers
}

func (f *fakeResolved) runList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

func (f *fakeResolved) resetRuns() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = nil
}

// defaultRoute is what "resolvectl default-route" prints: the setting, or when
// there is none, yes for a link with servers that is not for particular domains
// only.
func (l *fakeResolvedLink) defaultRoute() bool {
	if l.route != "" {
		return l.route == "yes"
	}
	return len(l.servers) > 0 && (len(l.domains) == 0 || slices.Contains(l.domains, "~."))
}

func (f *fakeResolved) run(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, name+" "+strings.Join(args, " "))
	if !f.programs[name] {
		return nil, fmt.Errorf("fork/exec %s: %w", name, fs.ErrNotExist)
	}
	if stdin != "" {
		return nil, errors.New("the adapter has no stdin to give")
	}
	out, ok := f.execute(args)
	if !ok {
		return []byte(out), errors.New("exit status 1")
	}
	return []byte(out), nil
}

var fakeDNSDomain = regexp.MustCompile(`^~?(\.|[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*)$`)

// execute runs one resolvectl command; ok is false when it exits with status 1.
func (f *fakeResolved) execute(args []string) (out string, ok bool) {
	verb := args[0]
	// Like getopt: what precedes "--" and starts with "-" is an option.
	var pos []string
	dashdash := false
	for _, a := range args[1:] {
		switch {
		case dashdash:
			pos = append(pos, a)
		case a == "--":
			dashdash = true
		case strings.HasPrefix(a, "-"):
			return fmt.Sprintf("resolvectl: unrecognized option '%s'\n", a), false
		default:
			pos = append(pos, a)
		}
	}
	if f.down {
		return printedNoBus, false
	}
	switch verb {
	case "flush-caches":
		return "", true
	case "dns", "domain", "default-route", "revert":
	default:
		return fmt.Sprintf("Unknown command verb '%s'.\n", verb), false
	}
	if len(pos) == 0 {
		if verb != "dns" {
			return "Failed to parse arguments\n", false
		}
		return f.list()
	}
	name := pos[0]
	link := f.links[name]
	if link == nil {
		return fmt.Sprintf("Failed to resolve interface %q: No such device\n", name), false
	}
	setter := len(pos) > 1 || verb == "revert"
	if !link.known {
		if setter {
			return fmt.Sprintf("Failed to set configuration: Link %d not known\n", link.index), false
		}
		return fmt.Sprintf("Failed to get link data for %d: Unknown object '/org/freedesktop/resolve1/link/_3%d'.\n", link.index, link.index), false
	}
	if !setter {
		return f.describe(name, link, verb), true
	}
	if verb == "revert" {
		if f.failRevert != "" {
			return f.failRevert, false
		}
		link.servers, link.domains, link.route = nil, nil, ""
		return "", true
	}
	if msg := f.failWrite[verb]; msg != "" {
		return msg, false
	}
	values := pos[1:]
	switch verb {
	case "dns":
		for _, v := range values {
			if _, err := netip.ParseAddr(v); err != nil {
				return fmt.Sprintf("Failed to parse DNS server address: %s\nFailed to set DNS configuration: Invalid argument\n", v), false
			}
		}
		if !f.ignoreWrite[verb] {
			link.servers = slices.Clone(values)
		}
	case "domain":
		for _, v := range values {
			if !fakeDNSDomain.MatchString(v) {
				return "Failed to set DNS domain configuration: Invalid argument\n", false
			}
		}
		if !f.ignoreWrite[verb] {
			link.domains = slices.Clone(values)
		}
	case "default-route":
		if len(values) != 1 || values[0] != "yes" && values[0] != "no" {
			return fmt.Sprintf("Failed to parse boolean argument: %s\n", strings.Join(values, " ")), false
		}
		if !f.ignoreWrite[verb] {
			link.route = values[0]
		}
	}
	return "", true
}

// list is "resolvectl dns" without an interface.
func (f *fakeResolved) list() (string, bool) {
	var errs, lines strings.Builder
	lines.WriteString("Global:\n")
	names := make([]string, 0, len(f.links))
	for name := range f.links {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return f.links[a].index - f.links[b].index })
	for _, name := range names {
		l := f.links[name]
		if !l.known {
			fmt.Fprintf(&errs, "Failed to get link data for %d: Unknown object '/org/freedesktop/resolve1/link/_3%d'.\n", l.index, l.index)
			continue
		}
		lines.WriteString(f.describe(name, l, "dns"))
	}
	return errs.String() + lines.String(), errs.Len() == 0
}

func (f *fakeResolved) describe(name string, l *fakeResolvedLink, verb string) string {
	var words []string
	switch verb {
	case "dns":
		words = l.servers
	case "domain":
		words = l.domains
	case "default-route":
		words = []string{map[bool]string{true: "yes", false: "no"}[l.defaultRoute()]}
	}
	line := fmt.Sprintf("Link %d (%s):", l.index, name)
	if len(words) > 0 {
		line += " " + strings.Join(words, " ")
	}
	return line + "\n"
}

func newTestDNS(f *fakeResolved) *dnsConfigurator {
	return newDNSConfigurator(DNSOptions{Run: f.run, Logger: dnsTestLog()})
}

func dnsCatchAll(iface string, servers ...string) osnet.DNSEntry {
	return osnet.DNSEntry{Servers: dnsAddrs(servers...), MatchDomains: []string{"."}, Iface: iface}
}

func dnsSplit(iface string, servers string, domains ...string) osnet.DNSEntry {
	return osnet.DNSEntry{Servers: dnsAddrs(servers), MatchDomains: domains, Iface: iface}
}

func TestCatchAllCommands(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1", "fd00::1")}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/resolvectl dns",
		"/usr/bin/resolvectl default-route -- tun0 yes",
		"/usr/bin/resolvectl domain -- tun0 ~.",
		"/usr/bin/resolvectl dns -- tun0 10.6.0.1 fd00::1",
		"/usr/bin/resolvectl dns -- tun0",
		"/usr/bin/resolvectl domain -- tun0",
		"/usr/bin/resolvectl default-route -- tun0",
	}
	if got := f.runList(); !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, want := f.config("tun0"), (linkConfig{Servers: []string{"10.6.0.1", "fd00::1"}, Domains: []string{"~."}, DefaultRoute: true}); !got.equal(want) {
		t.Errorf("link is %v, want %v", got, want)
	}
}

func TestSplitDNSCommands(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsSplit("tun0", "10.6.0.53", "corp.example.com", "lan")}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/resolvectl dns",
		"/usr/bin/resolvectl default-route -- tun0 no",
		"/usr/bin/resolvectl domain -- tun0 ~corp.example.com ~lan",
		"/usr/bin/resolvectl dns -- tun0 10.6.0.53",
		"/usr/bin/resolvectl dns -- tun0",
		"/usr/bin/resolvectl domain -- tun0",
		"/usr/bin/resolvectl default-route -- tun0",
	}
	if got := f.runList(); !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := f.config("tun0"); got.DefaultRoute {
		t.Errorf("a split link is a default route: %v", got)
	}
}

// Names in the shapes profiles and servers have are normalized, and a name
// that is a routing domain already is not made one twice.
func TestApplyAcceptsWhatProfilesContain(t *testing.T) {
	f := newFakeResolved("tun0")
	err := newTestDNS(f).Apply("a1b2c3d4-e5f6", []osnet.DNSEntry{{
		Iface:        "tun0",
		Servers:      dnsAddrs("10.6.0.1", "::ffff:10.6.0.2", "2001:db8::53"),
		MatchDomains: []string{"Corp.Example.COM.", "~lan", "_sip._tcp.example.com", "xn--r8jz45g.jp", "168.192.in-addr.arpa", "a", "corp.example.com"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := linkConfig{
		Servers: []string{"10.6.0.1", "10.6.0.2", "2001:db8::53"},
		Domains: []string{"~corp.example.com", "~lan", "~_sip._tcp.example.com", "~xn--r8jz45g.jp", "~168.192.in-addr.arpa", "~a"},
	}
	if got := f.config("tun0"); !got.equal(want) {
		t.Errorf("link is %v, want %v", got, want)
	}
}

func TestApplyMergesEntriesOfOneInterface(t *testing.T) {
	f := newFakeResolved("tun0", "tun1")
	d := newTestDNS(f)
	err := d.Apply("office", []osnet.DNSEntry{
		{Servers: dnsAddrs("10.6.0.2", "10.6.0.3"), MatchDomains: []string{"b.example"}, Order: 200, Iface: "tun0"},
		{Servers: dnsAddrs("10.6.0.1", "10.6.0.2"), MatchDomains: []string{"a.example", "b.example"}, Order: 100, Iface: "tun0"},
		{Servers: dnsAddrs("10.7.0.1"), MatchDomains: []string{"c.example"}, Order: 150, Iface: "tun1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := linkConfig{Servers: []string{"10.6.0.1", "10.6.0.2", "10.6.0.3"}, Domains: []string{"~a.example", "~b.example"}}
	if got := f.config("tun0"); !got.equal(want) {
		t.Errorf("tun0 is %v, want %v", got, want)
	}
	if got, want := f.config("tun1"), (linkConfig{Servers: []string{"10.7.0.1"}, Domains: []string{"~c.example"}}); !got.equal(want) {
		t.Errorf("tun1 is %v, want %v", got, want)
	}

	// One of the entries matches everything: the link is the default route.
	err = d.Apply("office", []osnet.DNSEntry{
		dnsSplit("tun0", "10.6.0.1", "a.example"),
		dnsCatchAll("tun0", "10.6.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.config("tun0"); !got.DefaultRoute || !slices.Equal(got.Servers, []string{"10.6.0.1"}) {
		t.Errorf("tun0 is %v, want the default route to 10.6.0.1", got)
	}
}

func TestApplyReplacesWhatIsThere(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1", "10.6.0.2")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply("office", []osnet.DNSEntry{dnsSplit("tun0", "10.6.0.9", "lan")}); err != nil {
		t.Fatal(err)
	}
	want := linkConfig{Servers: []string{"10.6.0.9"}, Domains: []string{"~lan"}}
	if got := f.config("tun0"); !got.equal(want) {
		t.Errorf("link is %v, want %v", got, want)
	}
}

func TestApplyMovesToAnotherInterface(t *testing.T) {
	f := newFakeResolved("tun0", "tun1")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun1", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if f.isConfigured("tun0") {
		t.Errorf("tun0 still holds %v", f.config("tun0"))
	}
	if got := f.config("tun1"); !got.DefaultRoute || !slices.Equal(got.Servers, []string{"10.6.0.1"}) {
		t.Errorf("tun1 is %v", got)
	}
	if owned, err := d.Owned(); err != nil || !slices.Equal(owned, []string{"plaitway:office:tun1"}) {
		t.Errorf("Owned = %v, %v, want only tun1", owned, err)
	}

	// The old interface may be gone by then.
	f.addLink("tun2")
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun2", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.vanish("tun2")
	f.addLink("tun3")
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun3", "10.6.0.1")}); err != nil {
		t.Fatalf("moving away from a link that is gone: %v", err)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	entries := []osnet.DNSEntry{
		{Servers: dnsAddrs("10.6.0.1", "10.6.0.2"), MatchDomains: []string{".", "corp.example"}, Iface: "tun0"},
	}
	for range 3 {
		if err := d.Apply("office", entries); err != nil {
			t.Fatal(err)
		}
	}
	want := linkConfig{Servers: []string{"10.6.0.1", "10.6.0.2"}, Domains: []string{"~.", "~corp.example"}, DefaultRoute: true}
	if got := f.config("tun0"); !got.equal(want) {
		t.Errorf("link is %v, want %v", got, want)
	}
	if owned, err := d.Owned(); err != nil || !slices.Equal(owned, []string{"plaitway:office:tun0"}) {
		t.Errorf("Owned = %v, %v", owned, err)
	}
}

func TestApplyOfNothingRemoves(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply("office", nil); err != nil {
		t.Fatal(err)
	}
	if f.isConfigured("tun0") {
		t.Errorf("tun0 still holds %v", f.config("tun0"))
	}
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned = %v, %v", owned, err)
	}
}

func TestRemove(t *testing.T) {
	f := newFakeResolved("tun0", "tun1", "eth0")
	f.setServers("eth0", "192.0.2.1")
	d := newTestDNS(f)
	for owner, iface := range map[string]string{"office": "tun0", "home": "tun1"} {
		if err := d.Apply(owner, []osnet.DNSEntry{dnsCatchAll(iface, "10.6.0.1")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	if f.isConfigured("tun0") {
		t.Error("office is still there")
	}
	if !f.isConfigured("tun1") {
		t.Error("home was removed too")
	}
	if got := f.config("eth0"); !slices.Equal(got.Servers, []string{"192.0.2.1"}) {
		t.Errorf("a link that is not ours was changed: %v", got)
	}

	// Removing nothing is not an error and runs nothing.
	f.resetRuns()
	for _, nothing := range []string{"office", "never-applied", "plaitway:never:tun0"} {
		if err := d.Remove(nothing); err != nil {
			t.Errorf("Remove(%q): %v", nothing, err)
		}
	}
	if runs := f.runList(); len(runs) != 0 {
		t.Errorf("resolvectl ran for nothing: %q", runs)
	}
}

// The Reconciler's sweep of leftovers hands Remove the keys that Owned found.
func TestRemoveAcceptsAKeyFromOwned(t *testing.T) {
	f := newFakeResolved("tun0", "tun1", "tun2")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1"), dnsSplit("tun1", "10.7.0.1", "lab.example")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply("home", []osnet.DNSEntry{dnsCatchAll("tun2", "10.8.0.1")}); err != nil {
		t.Fatal(err)
	}
	owned, err := d.Owned()
	if want := []string{"plaitway:home:tun2", "plaitway:office:tun0", "plaitway:office:tun1"}; err != nil || !slices.Equal(owned, want) {
		t.Fatalf("Owned = %v, %v, want %v", owned, err, want)
	}
	// A key stands for its owner, so the first key of office takes both links.
	if err := d.Remove("plaitway:office:tun0"); err != nil {
		t.Fatalf("Remove(key): %v", err)
	}
	if f.isConfigured("tun0") || f.isConfigured("tun1") {
		t.Error("a link of office survived the removal of its key")
	}
	if !f.isConfigured("tun2") {
		t.Error("home was removed with office")
	}
	if err := d.Remove("plaitway:office:tun1"); err != nil {
		t.Fatalf("Remove of the second key: %v", err)
	}

	// The sweep: list, remove each, list again.
	owned, _ = d.Owned()
	for _, key := range owned {
		if err := d.Remove(key); err != nil {
			t.Fatal(err)
		}
	}
	if left, err := d.Owned(); err != nil || len(left) != 0 {
		t.Errorf("Owned after the sweep = %v, %v", left, err)
	}
	if f.isConfigured("tun2") {
		t.Error("the sweep left home's link configured")
	}
}

func TestOwnedKeys(t *testing.T) {
	f := newFakeResolved("tun0", "wg-1")
	d := newTestDNS(f)
	odd := "profile: a/b%c d"
	if err := d.Apply(odd, []osnet.DNSEntry{dnsCatchAll("wg-1", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.7.0.1")}); err != nil {
		t.Fatal(err)
	}
	owned, err := d.Owned()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"plaitway:office:tun0", "plaitway:profile%3A+a%2Fb%25c+d:wg-1"}; !slices.Equal(owned, want) {
		t.Errorf("Owned = %q, want %q", owned, want)
	}
	// An owner with odd characters is removed through its key as well.
	if err := d.Remove(owned[1]); err != nil {
		t.Fatal(err)
	}
	if f.isConfigured("wg-1") {
		t.Error("the link of the odd owner is still configured")
	}
	if !f.isConfigured("tun0") {
		t.Error("office was removed")
	}
}

func TestOwnedForgetsALinkThatIsGone(t *testing.T) {
	f := newFakeResolved("tun0", "tun1")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1"), dnsCatchAll("tun1", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.vanish("tun0")
	owned, err := d.Owned()
	if err != nil || !slices.Equal(owned, []string{"plaitway:office:tun1"}) {
		t.Fatalf("Owned = %v, %v, want only tun1", owned, err)
	}
	f.vanish("tun1")
	if owned, err = d.Owned(); err != nil || len(owned) != 0 {
		t.Fatalf("Owned = %v, %v, want nothing", owned, err)
	}
	// Nothing is left to revert, and nothing is run for it.
	f.resetRuns()
	if err := d.Remove("office"); err != nil {
		t.Errorf("Remove: %v", err)
	}
	if runs := f.runList(); len(runs) != 0 {
		t.Errorf("resolvectl ran for nothing: %q", runs)
	}
}

// A link that was configured by somebody else since is not ours any more, and
// reverting it would destroy their configuration.
func TestOwnedForgetsALinkConfiguredByOthers(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.setServers("tun0", "9.9.9.9")
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Fatalf("Owned = %v, %v, want nothing", owned, err)
	}
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	if got := f.config("tun0"); !slices.Equal(got.Servers, []string{"9.9.9.9"}) {
		t.Errorf("their configuration was reverted: %v", got)
	}
}

func TestRemoveOfAVanishedLinkSucceeds(t *testing.T) {
	f := newFakeResolved("tun0", "tun1")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1"), dnsCatchAll("tun1", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.vanish("tun0")
	if err := d.Remove("office"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if f.isConfigured("tun1") {
		t.Error("the link that is still there was not reverted")
	}
	if owned, _ := d.Owned(); len(owned) != 0 {
		t.Errorf("Owned = %v", owned)
	}
}

func TestResolvedIsNotRunning(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.down = true

	err := d.Apply("lab", []osnet.DNSEntry{dnsCatchAll("tun0", "10.7.0.1")})
	if !errors.Is(err, errNoResolved) || !strings.Contains(err.Error(), "systemd-resolved is not running") {
		t.Errorf("Apply = %v, want systemd-resolved is not running", err)
	}
	if err := d.Flush(); !errors.Is(err, errNoResolved) {
		t.Errorf("Flush = %v, want systemd-resolved is not running", err)
	}
	if err := d.Remove("office"); err != nil {
		t.Errorf("Remove = %v, want success", err)
	}
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned = %v, %v, want nothing", owned, err)
	}

	// With nothing applied, nothing is asked of resolvectl.
	f.resetRuns()
	if err := d.Remove("office"); err != nil {
		t.Error(err)
	}
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned = %v, %v", owned, err)
	}
	if runs := f.runList(); len(runs) != 0 {
		t.Errorf("resolvectl ran for nothing: %q", runs)
	}

	// resolved is back with nothing of ours: its settings did not survive.
	f.down = false
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned after it came back = %v, %v", owned, err)
	}
}

func TestOwnedWithResolvedDownForgetsEverything(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.down = true
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Fatalf("Owned = %v, %v, want nothing", owned, err)
	}
	f.down = false
	f.resetRuns()
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	if runs := f.runList(); len(runs) != 0 {
		t.Errorf("Remove reverted what resolved lost: %q", runs)
	}
}

func TestResolvectlIsMissing(t *testing.T) {
	f := newFakeResolved("tun0")
	clear(f.programs)
	d := newTestDNS(f)

	err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")})
	if !errors.Is(err, errNoResolvectl) || !strings.Contains(err.Error(), "resolvectl not found") {
		t.Errorf("Apply = %v, want resolvectl not found", err)
	}
	if err := d.Flush(); !errors.Is(err, errNoResolvectl) {
		t.Errorf("Flush = %v, want resolvectl not found", err)
	}
	if err := d.Remove("office"); err != nil {
		t.Errorf("Remove = %v, want success", err)
	}
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned = %v, %v, want nothing", owned, err)
	}
	if runs := f.runList(); !slices.Contains(runs, "/usr/bin/resolvectl dns") || !slices.Contains(runs, "/bin/resolvectl dns") {
		t.Errorf("both paths should have been tried: %q", runs)
	}
}

// A host with neither resolved nor resolvconf is told what to install, in the
// reason the profile shows.
func TestResolvectlIsMissingAndSoIsResolvconf(t *testing.T) {
	f := newFakeResolved("tun0")
	clear(f.programs)
	d := newTestDNS(f)
	d.noResolvconf = true
	err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")})
	if !errors.Is(err, errNoResolvectl) || !strings.Contains(err.Error(), "resolvectl not found") || !strings.Contains(err.Error(), "no resolvconf") {
		t.Errorf("Apply = %v, want resolvectl not found, and no resolvconf", err)
	}
}

func TestResolvectlInBin(t *testing.T) {
	f := newFakeResolved("tun0")
	delete(f.programs, resolvectlPath)
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if !f.isConfigured("tun0") {
		t.Error("nothing was written")
	}
}

// A tool that hangs is neither a missing nor a stopped resolved.
func TestACommandThatTimesOut(t *testing.T) {
	hang := func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		<-ctx.Done()
		return nil, errors.New("signal: killed")
	}
	d := newDNSConfigurator(DNSOptions{Run: hang, Logger: dnsTestLog()})
	d.timeout = 20 * time.Millisecond
	err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")})
	if !errors.Is(err, errTimedOut) || errors.Is(err, errNoResolved) {
		t.Errorf("Apply = %v, want a timeout", err)
	}
}

// resolved knows only the links the kernel announced to it, and a tunnel is
// announced a moment after it is made.
func TestALinkResolvedDoesNotKnowYet(t *testing.T) {
	f := newFakeResolved("tun0", "tun1")
	f.links["tun1"].known = false
	d := newTestDNS(f)

	// "resolvectl dns" fails because of tun1; that is not a resolved that is down.
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatalf("Apply to a link that resolved knows: %v", err)
	}
	err := d.Apply("lab", []osnet.DNSEntry{dnsCatchAll("tun1", "10.7.0.1")})
	if err == nil || errors.Is(err, errNoResolved) || !strings.Contains(err.Error(), "tun1") || !strings.Contains(err.Error(), "not known") {
		t.Fatalf("Apply to an unknown link = %v, want the answer of resolvectl", err)
	}
	f.links["tun1"].known = true
	if err := d.Apply("lab", []osnet.DNSEntry{dnsCatchAll("tun1", "10.7.0.1")}); err != nil {
		t.Fatalf("Apply once resolved knows the link: %v", err)
	}
	if owned, err := d.Owned(); err != nil || len(owned) != 2 {
		t.Errorf("Owned = %v, %v", owned, err)
	}
}

func TestApplyUndoesAFailedWrite(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsSplit("tun0", "10.6.0.1", "old.example")}); err != nil {
		t.Fatal(err)
	}
	f.failWrite["dns"] = "Failed to set DNS configuration: Access denied\n"
	err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.2")})
	if err == nil || !strings.Contains(err.Error(), "Access denied") || !strings.Contains(err.Error(), "tun0") {
		t.Fatalf("Apply = %v, want the answer of resolvectl", err)
	}
	if f.isConfigured("tun0") {
		t.Errorf("a half-written link is left: %v", f.config("tun0"))
	}
	if owned, _ := d.Owned(); len(owned) != 0 {
		t.Errorf("Owned = %v", owned)
	}

	// The next attempt starts from nothing.
	delete(f.failWrite, "dns")
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.2")}); err != nil {
		t.Fatal(err)
	}
}

// resolvectl exits 0 for some settings it does not apply, so Apply reads them
// back.
func TestApplyNoticesAWriteThatDidNotHappen(t *testing.T) {
	f := newFakeResolved("tun0")
	f.ignoreWrite["domain"] = true
	d := newTestDNS(f)
	err := d.Apply("office", []osnet.DNSEntry{dnsSplit("tun0", "10.6.0.1", "corp.example")})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"resolved reports", "domains []", "default route", "wanted", "~corp.example"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if f.isConfigured("tun0") {
		t.Errorf("the link was left configured: %v", f.config("tun0"))
	}
	if owned, _ := d.Owned(); len(owned) != 0 {
		t.Errorf("Owned = %v", owned)
	}
}

func TestAFailedRevertStaysTrackedForTheNextAttempt(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.failRevert = "Failed to revert interface configuration: Access denied\n"
	err := d.Remove("office")
	if err == nil || !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("Remove = %v, want the answer of resolvectl", err)
	}
	if owned, err := d.Owned(); err != nil || !slices.Equal(owned, []string{"plaitway:office:tun0"}) {
		t.Errorf("Owned = %v, %v: the link still carries our servers", owned, err)
	}
	f.failRevert = ""
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	if f.isConfigured("tun0") {
		t.Error("the second attempt did not revert the link")
	}
}

func TestFlush(t *testing.T) {
	f := newFakeResolved()
	if err := newTestDNS(f).Flush(); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/resolvectl dns", "/usr/bin/resolvectl flush-caches"}
	if got := f.runList(); !slices.Equal(got, want) {
		t.Errorf("commands %q, want %q", got, want)
	}
}

func TestApplyRejectsInvalidInput(t *testing.T) {
	servers := dnsAddrs("10.6.0.1")
	good := func(mutate func(*osnet.DNSEntry)) []osnet.DNSEntry {
		e := osnet.DNSEntry{Servers: servers, MatchDomains: []string{"corp.example"}, Iface: "tun0"}
		mutate(&e)
		return []osnet.DNSEntry{e}
	}
	iface := func(name string) []osnet.DNSEntry { return good(func(e *osnet.DNSEntry) { e.Iface = name }) }
	domain := func(name string) []osnet.DNSEntry {
		return good(func(e *osnet.DNSEntry) { e.MatchDomains = []string{"ok.example", name} })
	}
	server := func(a netip.Addr) []osnet.DNSEntry {
		return good(func(e *osnet.DNSEntry) { e.Servers = []netip.Addr{netip.MustParseAddr("10.6.0.1"), a} })
	}
	long := strings.Repeat("a", 64)
	tests := []struct {
		name    string
		owner   string
		entries []osnet.DNSEntry
		field   string // what the error names
	}{
		{"empty owner", "", good(func(*osnet.DNSEntry) {}), "owner"},
		{"owner that is a key", "plaitway:office:tun0", good(func(*osnet.DNSEntry) {}), "owner"},

		{"no interface", "o", iface(""), "interface"},
		{"interface that is an option", "o", iface("-n"), "interface"},
		{"interface that is a long option", "o", iface("--version"), "interface"},
		{"interface that is a dash", "o", iface("-"), "interface"},
		{"interface with a space", "o", iface("tun0 -n"), "interface"},
		{"interface with a newline", "o", iface("tun0\n"), "interface"},
		{"interface with a tab", "o", iface("tun\t0"), "interface"},
		{"interface with a NUL", "o", iface("tun0\x00"), "interface"},
		{"interface with a slash", "o", iface("tun/0"), "interface"},
		{"interface with a colon", "o", iface("tun:0"), "interface"},
		{"interface that is a dot", "o", iface("."), "interface"},
		{"interface that is two dots", "o", iface(".."), "interface"},
		{"interface that is too long", "o", iface("0123456789abcdef"), "interface"},
		{"interface that is a number", "o", iface("7"), "interface"},
		{"interface that is a signed number", "o", iface("+7"), "interface"},
		{"interface that is a hex number", "o", iface("0x7"), "interface"},

		{"no servers", "o", good(func(e *osnet.DNSEntry) { e.Servers = nil }), "no servers"},
		{"invalid server", "o", server(netip.Addr{}), "server"},
		{"unspecified server", "o", server(netip.MustParseAddr("0.0.0.0")), "server"},
		{"unspecified IPv6 server", "o", server(netip.MustParseAddr("::")), "server"},
		{"multicast server", "o", server(netip.MustParseAddr("ff02::fb")), "server"},
		{"server with a zone", "o", server(netip.MustParseAddr("fe80::1%eth0")), "server"},

		{"no domains", "o", good(func(e *osnet.DNSEntry) { e.MatchDomains = nil }), "no match domains"},
		{"empty domain", "o", domain(""), "domain"},
		{"domain that is an option", "o", domain("--help"), "domain"},
		{"domain starting with a hyphen", "o", domain("-a.example"), "domain"},
		{"domain that is a hyphen", "o", domain("-"), "domain"},
		{"routing mark of an option", "o", domain("~-a.example"), "domain"},
		{"doubled routing mark", "o", domain("~~a.example"), "domain"},
		{"only a routing mark", "o", domain("~"), "domain"},
		{"domain with a space", "o", domain("a b.example"), "domain"},
		{"domain with a newline", "o", domain("a.example\n--help"), "domain"},
		{"domain with a tab", "o", domain("a\t.example"), "domain"},
		{"domain with a semicolon", "o", domain("a;b.example"), "domain"},
		{"domain with a substitution", "o", domain("$(id).example"), "domain"},
		{"domain with backticks", "o", domain("`id`.example"), "domain"},
		{"domain with a quote", "o", domain(`a".example`), "domain"},
		{"domain with a slash", "o", domain("a/b.example"), "domain"},
		{"domain with a backslash", "o", domain(`a\.b.example`), "domain"},
		{"wildcard domain", "o", domain("*.example"), "domain"},
		{"domain with an empty label", "o", domain("a..example"), "domain"},
		{"domain with two trailing dots", "o", domain("a.example.."), "domain"},
		{"domain starting with a dot", "o", domain(".example"), "domain"},
		{"two dots", "o", domain(".."), "domain"},
		{"label starting with a hyphen", "o", domain("a.-b.example"), "domain"},
		{"label ending with a hyphen", "o", domain("a-.example"), "domain"},
		{"label that is too long", "o", domain(long + ".example"), "domain"},
		{"domain that is too long", "o", domain(strings.Repeat("abcdefgh.", 31) + "com"), "domain"},
		{"non-ASCII domain", "o", domain("例え.jp"), "domain"},
		{"IDN that is not punycode", "o", domain("bücher.example"), "domain"},
		{"letter that lower-cases to ASCII", "o", domain("Korp.example"), "domain"},
		{"domain with a NUL", "o", domain("a\x00.example"), "domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeResolved("tun0")
			err := newTestDNS(f).Apply(tt.owner, tt.entries)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Errorf("error %q does not name %q", err, tt.field)
			}
			if runs := f.runList(); len(runs) != 0 {
				t.Errorf("resolvectl ran: %q", runs)
			}
			if f.isConfigured("tun0") {
				t.Error("something was written")
			}
		})
	}

	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	for _, owner := range []string{"", "plaitway:a"} {
		if err := d.Remove(owner); err == nil {
			t.Errorf("Remove(%q) accepted an invalid owner", owner)
		}
	}
	// A key that is not one is an owner that is not valid.
	if err := d.Remove("plaitway:office:tun/0"); err == nil {
		t.Error("Remove accepted a key with an invalid interface")
	}
	if runs := f.runList(); len(runs) != 0 {
		t.Errorf("resolvectl ran: %q", runs)
	}
}

// Valid values cannot be read as options either: "--" precedes every
// interface and everything after it.
func TestArgumentsFollowTheEndOfOptions(t *testing.T) {
	f := newFakeResolved("tun0")
	d := newTestDNS(f)
	if err := d.Apply("office", []osnet.DNSEntry{{
		Servers:      dnsAddrs("10.6.0.1", "fd00::1"),
		MatchDomains: []string{".", "a-b.example", "_srv.example", "~x.example"},
		Iface:        "tun0",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove("office"); err != nil {
		t.Fatal(err)
	}
	verbs := map[string]bool{"dns": true, "domain": true, "default-route": true, "revert": true}
	checked := 0
	for _, run := range f.runList() {
		args := strings.Fields(run)[1:]
		if !verbs[args[0]] || len(args) == 1 {
			continue
		}
		checked++
		if args[1] != "--" {
			t.Errorf("%q: the interface is not behind --", run)
		}
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "-") {
				t.Errorf("%q: %q could be read as an option", run, a)
			}
		}
	}
	if checked < 7 {
		t.Errorf("only %d commands checked", checked)
	}
}

func TestNormalizeDomain(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	maxName := strings.Join([]string{label63, label63, label63, label63[:61]}, ".") // 253 bytes
	for in, want := range map[string]string{
		".":                     ".",
		"~.":                    ".",
		"corp.example":          "corp.example",
		"Corp.Example.COM.":     "corp.example.com",
		"~corp.example":         "corp.example",
		"~Corp.example.":        "corp.example",
		"a":                     "a",
		"_sip._tcp.example.com": "_sip._tcp.example.com",
		"xn--r8jz45g.jp":        "xn--r8jz45g.jp",
		"168.192.in-addr.arpa":  "168.192.in-addr.arpa",
		"a-b.example":           "a-b.example",
		strings.Repeat("a", 63): strings.Repeat("a", 63),
		maxName:                 maxName,
	} {
		got, err := normalizeDomain(in)
		if err != nil || got != want {
			t.Errorf("normalizeDomain(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
}

func TestValidateIface(t *testing.T) {
	for _, ok := range []string{"tun0", "wg-office", "wg_1", "a.b", "e1", "x1y", "0123456789abcde", "plaitway0", "bad", "dead", "1e3"} {
		if err := validateIface(ok); err != nil {
			t.Errorf("validateIface(%q) = %v", ok, err)
		}
	}
}

func TestKeyEncoding(t *testing.T) {
	for _, owner := range []string{
		"office", "a1b2c3d4-e5f6", "a:b", "a:b:c", "a/b", "a b", "100%", "%3A", "a%3Ab", "..", "ü", "x\ny", "a+b", "plaitway", "plaitway-office",
	} {
		for _, iface := range []string{"tun0", "wg-1", "a.b"} {
			key := dnsKey(owner, iface)
			got, ok := ownerOfKey(key)
			if !ok || got != owner {
				t.Errorf("ownerOfKey(%q) = %q, %v, want %q", key, got, ok, owner)
			}
			if validateOwner(owner) != nil {
				t.Errorf("owner %q is refused", owner)
			}
		}
	}
	// Two pairs never share a key: the escaped owner has no ':'.
	if dnsKey("a:b", "c") == dnsKey("a", "b:c") || dnsKey("a", "bc") == dnsKey("ab", "c") {
		t.Error("keys of different links are equal")
	}
	for _, notKey := range []string{
		"", "office", "plaitway:", "plaitway:office", "plaitway::tun0", "plaitway:office:", "plaitway:office:-n",
		"plaitway:office:tun/0", "plaitway:office:7", "plaitway:office:a:b", "plaitway:a:b:tun0",
		"plaitway:%zz:tun0", "plaitway:a%3ab:tun0", "plaitway:a b:tun0", "Plaitway:office:tun0", " plaitway:office:tun0",
	} {
		if owner, ok := ownerOfKey(notKey); ok {
			t.Errorf("ownerOfKey(%q) = %q, want none", notKey, owner)
		}
	}
}

// What resolvectl printed for real is read the way the adapter needs it.
func TestQueryReadsRealOutput(t *testing.T) {
	tests := []struct {
		name, verb, iface, out string
		fail                   bool
		want                   []string
		gone                   bool
	}{
		{"servers", "dns", "wg0", printedServers, false, []string{"198.51.100.1", "198.51.100.2", "2001:db8::1", "2001:db8::2"}, false},
		{"no domains", "domain", "eth0", printedNoDomains, false, nil, false},
		{"catch-all", "domain", "wg0", printedCatchAll, false, []string{"~."}, false},
		{"default route", "default-route", "wg0", printedDefaultRoute, false, []string{"yes"}, false},
		{"no such device", "dns", "nosuch", printedNoSuchDevice, true, nil, true},
		{"unknown to resolved", "domain", "d7", printedUnknownObject, true, nil, true},
		{"no bus", "dns", "wg0", printedNoBus, true, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(context.Context, string, []string, string) ([]byte, error) {
				if tt.fail {
					return []byte(tt.out), errors.New("exit status 1")
				}
				return []byte(tt.out), nil
			}
			d := newDNSConfigurator(DNSOptions{Run: run, Logger: dnsTestLog()})
			got, err := d.query(tt.verb, tt.iface)
			if tt.fail {
				if err == nil || errors.Is(err, errLinkGone) != tt.gone || !strings.Contains(err.Error(), strings.TrimSpace(tt.out)) {
					t.Errorf("query = %v, %v, want an error carrying the output (gone: %v)", got, err, tt.gone)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("query = %q, %v, want %q", got, err, tt.want)
			}
		})
	}

	t.Run("another link", func(t *testing.T) {
		run := func(context.Context, string, []string, string) ([]byte, error) { return []byte(printedServers), nil }
		d := newDNSConfigurator(DNSOptions{Run: run, Logger: dnsTestLog()})
		if got, err := d.query("dns", "eth0"); err == nil {
			t.Errorf("query = %q, want an error: the answer is about wg0", got)
		}
	})
	t.Run("not a link line", func(t *testing.T) {
		run := func(context.Context, string, []string, string) ([]byte, error) {
			return []byte("something else\n"), nil
		}
		d := newDNSConfigurator(DNSOptions{Run: run, Logger: dnsTestLog()})
		if got, err := d.query("dns", "wg0"); err == nil {
			t.Errorf("query = %q, want an error", got)
		}
	})
}

// The Global section is what shows that resolved answered, with or without the
// links it does not know.
func TestCheckResolvedReadsRealOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		fail bool
		want error
	}{
		{"all known", printedDNSAll, false, nil},
		{"some unknown", printedDNSAllWithUnknown, true, nil},
		{"no bus", printedNoBus, true, errNoResolved},
		{"bus without resolved", printedNoResolved, true, errNoResolved},
		{"nothing printed", "", true, errNoResolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(context.Context, string, []string, string) ([]byte, error) {
				if tt.fail {
					return []byte(tt.out), errors.New("exit status 1")
				}
				return []byte(tt.out), nil
			}
			d := newDNSConfigurator(DNSOptions{Run: run, Logger: dnsTestLog()})
			if err := d.checkResolved(); !errors.Is(err, tt.want) {
				t.Errorf("checkResolved = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCanonicalForms(t *testing.T) {
	servers := canonicalServers([]string{"10.6.0.1", "::ffff:10.6.0.2", "2001:DB8::1", "10.6.0.3:5353", "10.6.0.4#dns.example", "fe80::1%2"})
	if want := []string{"10.6.0.1", "10.6.0.2", "2001:db8::1", "10.6.0.3:5353", "10.6.0.4#dns.example", "fe80::1%2"}; !slices.Equal(servers, want) {
		t.Errorf("canonicalServers = %q, want %q", servers, want)
	}
	domains := canonicalDomains([]string{"~.", "~Corp.Example.", "search.example", "~lan"})
	if want := []string{"~.", "~corp.example", "search.example", "~lan"}; !slices.Equal(domains, want) {
		t.Errorf("canonicalDomains = %q, want %q", domains, want)
	}
}

// The Reconciler is single-threaded, but the daemon may flush from elsewhere.
func TestConcurrentUse(t *testing.T) {
	const workers = 8
	var names []string
	for i := range workers {
		names = append(names, "tun"+strconv.Itoa(i))
	}
	f := newFakeResolved(names...)
	d := newTestDNS(f)
	var wg sync.WaitGroup
	for i, iface := range names {
		owner := "owner" + strconv.Itoa(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if err := d.Apply(owner, []osnet.DNSEntry{dnsCatchAll(iface, "10.6.0.1")}); err != nil {
					t.Errorf("Apply: %v", err)
				}
				if _, err := d.Owned(); err != nil {
					t.Errorf("Owned: %v", err)
				}
				if err := d.Flush(); err != nil {
					t.Errorf("Flush: %v", err)
				}
				if err := d.Remove(owner); err != nil {
					t.Errorf("Remove: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if owned, err := d.Owned(); err != nil || len(owned) != 0 {
		t.Errorf("Owned = %v, %v", owned, err)
	}
	for _, iface := range names {
		if f.isConfigured(iface) {
			t.Errorf("%s is still configured", iface)
		}
	}
}
