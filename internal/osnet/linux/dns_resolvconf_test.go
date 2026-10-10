package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// fakeOpenresolv is the part of openresolv's resolvconf that the adapter uses,
// with the output and the exit statuses that openresolv 3.13.2 and 3.16.5 gave
// when the same commands were tried on them: "-i" lists the names that match a shell
// pattern and exits with 2 and a message when none does, "-l" prints the file of
// a name with a comment line before it, "-d" of a name that is not there fails
// unless "-f" is given.
type fakeOpenresolv struct {
	mu    sync.Mutex
	files map[string]string
	runs  []string
	// path is where the binary is; any other path is not there.
	path string
	// version is what "--version" prints.
	version string
	// failAdd fails the add of a name.
	failAdd func(name string) error
	// store changes what is stored for a name, to make the read back differ.
	store func(name, body string) string
	// hang makes a command wait for its context.
	hang bool
	// noFilesWord is what the message about a name without a file calls it:
	// "interface" in openresolv 3.13, "key" in 3.16.
	noFilesWord string
}

func newFakeOpenresolv() *fakeOpenresolv {
	return &fakeOpenresolv{files: map[string]string{}, path: "/sbin/resolvconf", version: "openresolv 3.13.2", noFilesWord: "interface"}
}

func (f *fakeOpenresolv) run(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name != f.path {
		return nil, fs.ErrNotExist
	}
	f.runs = append(f.runs, strings.Join(args, " "))
	if f.hang {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, ctx.Err()
	}
	exit := func(code int, out string) ([]byte, error) {
		return []byte(out), fmt.Errorf("exit status %d", code)
	}
	switch {
	case len(args) == 1 && args[0] == "--version":
		return []byte(f.version + "\n"), nil
	case len(args) == 2 && args[0] == "-i":
		var names []string
		for n := range f.files {
			if ok, _ := path.Match(args[1], n); ok {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return exit(2, "No resolv.conf for "+f.noFilesWord+" "+args[1]+"\n\n")
		}
		slices.Sort(names)
		return []byte(strings.Join(names, " ") + " \n"), nil
	case len(args) == 3 && args[0] == "-x" && args[1] == "-a":
		if f.failAdd != nil {
			if err := f.failAdd(args[2]); err != nil {
				return exit(1, err.Error()+"\n")
			}
		}
		body := stdin
		if f.store != nil {
			body = f.store(args[2], body)
		}
		f.files[args[2]] = body
		return nil, nil
	case len(args) == 2 && args[0] == "-l":
		body, ok := f.files[args[1]]
		if !ok {
			return exit(2, "No resolv.conf for "+f.noFilesWord+" "+args[1]+"\n\n")
		}
		return []byte("# resolv.conf from " + args[1] + "\n" + body + "\n"), nil
	case len(args) == 3 && args[0] == "-f" && args[1] == "-d":
		delete(f.files, args[2])
		return nil, nil
	case len(args) == 2 && args[0] == "-d":
		if _, ok := f.files[args[1]]; !ok {
			return exit(1, "No resolv.conf for "+f.noFilesWord+" "+args[1]+"\n")
		}
		delete(f.files, args[1])
		return nil, nil
	}
	return exit(1, "unexpected arguments: "+strings.Join(args, " ")+"\n")
}

func (f *fakeOpenresolv) runList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

func (f *fakeOpenresolv) resetRuns() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = nil
}

func (f *fakeOpenresolv) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.files))
	for n := range f.files {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (f *fakeOpenresolv) file(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[name]
}

func newTestResolvconf(f *fakeOpenresolv) *resolvconfConfigurator {
	return newResolvconfConfigurator(DNSOptions{Run: f.run, Logger: dnsTestLog()})
}

func TestResolvconfCatchAllCommands(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1", "fd00::1")}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--version",
		"-i plaitway:office:*",
		"-x -a plaitway:office:tun0",
		"-l plaitway:office:tun0",
	}
	if got := f.runList(); !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, want := f.file("plaitway:office:tun0"), "nameserver 10.6.0.1\nnameserver fd00::1\n"; got != want {
		t.Errorf("resolv.conf = %q, want %q", got, want)
	}
}

func TestResolvconfOrdersAndMergesTheEntriesOfOneInterface(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	entries := []osnet.DNSEntry{
		{Servers: dnsAddrs("10.7.0.53", "::ffff:10.7.0.54"), MatchDomains: []string{"corp.example"}, Order: 20, Iface: "tun1"},
		{Servers: dnsAddrs("10.7.0.52", "10.7.0.53"), MatchDomains: []string{"."}, Order: 10, Iface: "tun1"},
	}
	if err := d.Apply("lab", entries); err != nil {
		t.Fatal(err)
	}
	// Order 10 first, no server twice, an IPv4-mapped address as IPv4; the
	// domain of the other entry is covered by the catch-all.
	if got, want := f.file("plaitway:lab:tun1"), "nameserver 10.7.0.52\nnameserver 10.7.0.53\nnameserver 10.7.0.54\n"; got != want {
		t.Errorf("resolv.conf = %q, want %q", got, want)
	}
}

func TestResolvconfRefusesDNSForSomeDomainsOnly(t *testing.T) {
	for name, entries := range map[string][]osnet.DNSEntry{
		"split":                 {dnsSplit("tun0", "10.6.0.53", "corp.example.com", "lan")},
		"split and a catch-all": {dnsSplit("tun0", "10.6.0.53", "lan"), dnsCatchAll("tun1", "10.9.0.1")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeOpenresolv()
			d := newTestResolvconf(f)
			err := d.Apply("office", entries)
			if !errors.Is(err, errSplitDNS) || !strings.Contains(err.Error(), "lan") {
				t.Errorf("Apply = %v, want the split DNS error naming the domains", err)
			}
			if names := f.names(); len(names) != 0 {
				t.Errorf("entries = %v, want none: nothing is written for any interface", names)
			}
			for _, run := range f.runList() {
				if strings.Contains(run, "-a") || strings.Contains(run, "-d") {
					t.Errorf("ran %q, which changes resolvconf", run)
				}
			}
		})
	}
}

func TestResolvconfValidatesLikeResolved(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	for name, entry := range map[string]osnet.DNSEntry{
		"no servers":         {MatchDomains: []string{"."}, Iface: "tun0"},
		"no domains":         {Servers: dnsAddrs("10.0.0.1"), Iface: "tun0"},
		"a bad domain":       {Servers: dnsAddrs("10.0.0.1"), MatchDomains: []string{".", "bad domain"}, Iface: "tun0"},
		"a bad interface":    {Servers: dnsAddrs("10.0.0.1"), MatchDomains: []string{"."}, Iface: "-tun0"},
		"an unusable server": {Servers: dnsAddrs("0.0.0.0"), MatchDomains: []string{"."}, Iface: "tun0"},
	} {
		if err := d.Apply("office", []osnet.DNSEntry{entry}); err == nil {
			t.Errorf("%s: Apply succeeded", name)
		}
	}
	if err := d.Apply("plaitway:x", []osnet.DNSEntry{dnsCatchAll("tun0", "10.0.0.1")}); err == nil {
		t.Error("an owner that looks like a key was accepted")
	}
	if names := f.names(); len(names) != 0 {
		t.Errorf("entries = %v, want none", names)
	}
}

func TestResolvconfReplacesAndDropsAStaleInterface(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.resetRuns()
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun1", "10.7.0.1")}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.names(), []string{"plaitway:office:tun1"}; !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	runs := f.runList()
	add := slices.Index(runs, "-x -a plaitway:office:tun1")
	del := slices.Index(runs, "-f -d plaitway:office:tun0")
	if add < 0 || del < 0 || add > del {
		t.Errorf("commands = %v: the new interface goes in before the old one goes out", runs)
	}
}

func TestResolvconfApplyIsIdempotentAndOfNothingRemoves(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	entries := []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}
	for i := 0; i < 2; i++ {
		if err := d.Apply("office", entries); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := f.names(), []string{"plaitway:office:tun0"}; !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	if err := d.Apply("office", nil); err != nil {
		t.Fatal(err)
	}
	if names := f.names(); len(names) != 0 {
		t.Errorf("entries = %v, want none after Apply of nothing", names)
	}
}

func TestResolvconfRemoveTouchesOnlyTheOwner(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	f.files["eth0.dhcp"] = "nameserver 192.168.1.1\n"
	f.files["plaitway:a:tun0"] = "nameserver 10.0.0.1\n"
	// Names that only look like the owner's: another owner whose name starts the
	// same, and a name that is not the escaped one.
	f.files["plaitway:ab:tun0"] = "nameserver 10.0.0.2\n"
	if err := d.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if got, want := f.names(), []string{"eth0.dhcp", "plaitway:ab:tun0"}; !slices.Equal(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
	}
	if err := d.Remove("a"); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
}

func TestResolvconfRemoveAcceptsAKeyFromOwned(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	if err := d.Apply("office.home", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	keys, err := d.Owned()
	if err != nil || !slices.Equal(keys, []string{dnsKey("office.home", "tun0")}) {
		t.Fatalf("Owned = %v, %v", keys, err)
	}
	if err := d.Remove(keys[0]); err != nil {
		t.Fatal(err)
	}
	if names := f.names(); len(names) != 0 {
		t.Errorf("entries = %v, want none", names)
	}
}

func TestResolvconfOwnedFindsWhatACrashLeft(t *testing.T) {
	f := newFakeOpenresolv()
	// A daemon that was killed: its entries are still in the files of the host,
	// and a new Configurator has never heard of them.
	old := newTestResolvconf(f)
	if err := old.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	f.files["eth0.dhcp"] = "nameserver 192.168.1.1\n"
	// Names under the marker that Apply could not have written belong to somebody
	// else: no second part, a bad escape, an unescaped dot.
	f.files["plaitway:nointerface"] = "x\n"
	f.files["plaitway:a%zz:tun0"] = "x\n"
	f.files["plaitway:b:tun0.100"] = "x\n"

	fresh := newTestResolvconf(f)
	keys, err := fresh.Owned()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{dnsKey("office", "tun0")}; !slices.Equal(keys, want) {
		t.Fatalf("Owned = %v, want %v", keys, want)
	}
	if err := fresh.Remove(keys[0]); err != nil {
		t.Fatal(err)
	}
	if left := f.names(); slices.Contains(left, "plaitway:office:tun0") || !slices.Contains(left, "eth0.dhcp") || !slices.Contains(left, "plaitway:nointerface") {
		t.Errorf("entries = %v: only the entry of the daemon goes", left)
	}
}

func TestResolvconfNamesAreSafeAndReadBack(t *testing.T) {
	owners := []string{"office", "a.b", "a:b", "a*", "a?b", "[x]", "a b", "日本", "A/B", "-x", "100%", "a\nb"}
	for _, owner := range owners {
		name, err := resolvconfName(owner, "tun0")
		if err != nil {
			t.Errorf("%q: %v", owner, err)
			continue
		}
		rest := strings.TrimPrefix(name, "plaitway:")
		if strings.Count(rest, ":") != 1 || strings.ContainsAny(name, ". */?[]\\\n\t") {
			t.Errorf("%q: name %q has a character that resolvconf or a shell pattern reads", owner, name)
		}
		if o, i, ok := parseResolvconfName(name); !ok || o != owner || i != "tun0" {
			t.Errorf("%q: name %q reads back as %q, %q, %t", owner, name, o, i, ok)
		}
		// The pattern of this owner matches its names and the names of no other.
		for _, other := range owners {
			if other == owner {
				continue
			}
			otherName, _ := resolvconfName(other, "tun0")
			if ok, _ := path.Match(ownerPattern(owner), otherName); ok {
				t.Errorf("the pattern of %q matches the name of %q", owner, other)
			}
		}
		if ok, _ := path.Match(ownerPattern(owner), name); !ok {
			t.Errorf("the pattern of %q does not match its own name %q", owner, name)
		}
	}
	if _, err := resolvconfName(strings.Repeat("o", 300), "tun0"); err == nil {
		t.Error("a name of 300 characters was accepted")
	}
}

func TestResolvconfIsMissing(t *testing.T) {
	f := newFakeOpenresolv()
	f.path = "" // no path of resolvconf exists
	d := newTestResolvconf(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); !errors.Is(err, errNoResolvconf) || !strings.Contains(err.Error(), "resolvconf not found") {
		t.Errorf("Apply = %v, want resolvconf not found", err)
	}
	// Nothing was applied through it, so there is nothing to remove or to list.
	if err := d.Remove("office"); err != nil {
		t.Errorf("Remove = %v, want nil", err)
	}
	if keys, err := d.Owned(); err != nil || len(keys) != 0 {
		t.Errorf("Owned = %v, %v, want nothing", keys, err)
	}
	if err := d.Flush(); err != nil {
		t.Errorf("Flush = %v", err)
	}
}

func TestResolvconfThatIsNotOpenresolvIsNotDriven(t *testing.T) {
	f := newFakeOpenresolv()
	f.version = "resolvconf 1.91ubuntu1"
	d := newTestResolvconf(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); !errors.Is(err, errNotOpenresolv) {
		t.Errorf("Apply = %v, want %v", err, errNotOpenresolv)
	}
	for _, run := range f.runList() {
		if run != "--version" {
			t.Errorf("ran %q on a resolvconf that is not openresolv", run)
		}
	}
}

func TestResolvconfInUsrSbin(t *testing.T) {
	f := newFakeOpenresolv()
	f.path = "/usr/sbin/resolvconf"
	d := newTestResolvconf(f)
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if got := f.names(); !slices.Equal(got, []string{"plaitway:office:tun0"}) {
		t.Errorf("entries = %v", got)
	}
}

func TestResolvconfIsAskedForItsVersionOnce(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	for i := 0; i < 3; i++ {
		if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
			t.Fatal(err)
		}
	}
	if n := slices.Index(f.runList(), "--version"); n != 0 || strings.Count(strings.Join(f.runList(), "\n"), "--version") != 1 {
		t.Errorf("commands = %v: the version is asked once", f.runList())
	}
	// A binary that goes away is asked again when it is back.
	f.mu.Lock()
	f.path = ""
	f.mu.Unlock()
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); !errors.Is(err, errNoResolvconf) {
		t.Fatalf("Apply without resolvconf = %v", err)
	}
	f.mu.Lock()
	f.path = "/sbin/resolvconf"
	f.mu.Unlock()
	f.resetRuns()
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.runList(), "--version") {
		t.Errorf("commands = %v: the version is asked again", f.runList())
	}
}

func TestResolvconfRevertsWhatItCouldNotVerify(t *testing.T) {
	f := newFakeOpenresolv()
	f.store = func(name, body string) string { return "nameserver 203.0.113.9\n" } // something else is held
	d := newTestResolvconf(f)
	err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")})
	if err == nil || !strings.Contains(err.Error(), "holds servers") {
		t.Fatalf("Apply = %v, want a mismatch", err)
	}
	if names := f.names(); len(names) != 0 {
		t.Errorf("entries = %v, want none: half a configuration is reverted", names)
	}
}

func TestResolvconfRevertsWhenOneInterfaceFails(t *testing.T) {
	f := newFakeOpenresolv()
	f.failAdd = func(name string) error {
		if strings.HasSuffix(name, ":tun1") {
			return errors.New("cannot write")
		}
		return nil
	}
	d := newTestResolvconf(f)
	entries := []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1"), dnsCatchAll("tun1", "10.7.0.1")}
	if err := d.Apply("office", entries); err == nil || !strings.Contains(err.Error(), "cannot write") {
		t.Fatalf("Apply = %v", err)
	}
	if names := f.names(); len(names) != 0 {
		t.Errorf("entries = %v, want none: the first interface is reverted too", names)
	}
}

func TestResolvconfThatDoesNotAnswerTimesOut(t *testing.T) {
	f := newFakeOpenresolv()
	f.hang = true
	d := newTestResolvconf(f)
	d.timeout = 20 * time.Millisecond
	if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); !errors.Is(err, errTimedOut) {
		t.Errorf("Apply = %v, want a timeout", err)
	}
}

func TestResolvconfFlushRunsNothing(t *testing.T) {
	f := newFakeOpenresolv()
	d := newTestResolvconf(f)
	if err := d.Flush(); err != nil || len(f.runList()) != 0 {
		t.Errorf("Flush = %v after %v", err, f.runList())
	}
}

func TestResolvconfNothingToListIsNotAnErrorInEveryVersion(t *testing.T) {
	for _, word := range []string{"interface", "key"} {
		t.Run(word, func(t *testing.T) {
			f := newFakeOpenresolv()
			f.noFilesWord = word
			d := newTestResolvconf(f)
			if err := d.Apply("office", []osnet.DNSEntry{dnsCatchAll("tun0", "10.6.0.1")}); err != nil {
				t.Fatalf("Apply with no entry yet: %v", err)
			}
			if err := d.Remove("office"); err != nil {
				t.Fatalf("Remove: %v", err)
			}
			if err := d.Remove("other"); err != nil {
				t.Fatalf("Remove of an owner that has nothing: %v", err)
			}
			if keys, err := d.Owned(); err != nil || len(keys) != 0 {
				t.Fatalf("Owned = %v, %v; want nothing", keys, err)
			}
		})
	}
}
