package winiface

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"
)

func prefixes(texts ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(texts))
	for i, text := range texts {
		out[i] = netip.MustParsePrefix(text)
	}
	return out
}

// fakeSystem is an adapter table that behaves like the parts of the IP Helper
// API that this package depends on.
type fakeSystem struct {
	mu        sync.Mutex
	names     map[string]uint64
	index     map[uint64]uint32
	addrs     []address
	ops       []string
	tentative int // polls of addressState that still say tentative
	duplicate bool
	up        bool
	ifaces    map[Family]*ipInterface
	failOn    map[string]error
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		names:  map[string]uint64{"Plaitway-test-0": 0xABCD},
		index:  map[uint64]uint32{0xABCD: 42},
		ifaces: map[Family]*ipInterface{IPv4: {MTU: 1500, Metric: 25, AutoMetric: true}, IPv6: {MTU: 1500, Metric: 25, AutoMetric: true}},
		failOn: map[string]error{},
	}
}

func useFakeSystem(t *testing.T) *fakeSystem {
	t.Helper()
	fake := newFakeSystem()
	old, oldDAD, oldPoll, oldUp := host, dadTimeout, dadPollInterval, upPollInterval
	host, dadTimeout, dadPollInterval, upPollInterval = fake, 200*time.Millisecond, time.Millisecond, time.Millisecond
	t.Cleanup(func() { host, dadTimeout, dadPollInterval, upPollInterval = old, oldDAD, oldPoll, oldUp })
	return fake
}

func (f *fakeSystem) record(op string) error {
	f.ops = append(f.ops, op)
	return f.failOn[op]
}

func (f *fakeSystem) luidFromName(name string) (uint64, error) {
	if luid, ok := f.names[name]; ok {
		return luid, nil
	}
	return 0, ErrNotFound
}

func (f *fakeSystem) indexFromLUID(luid uint64) (uint32, error) {
	if i, ok := f.index[luid]; ok {
		return i, nil
	}
	return 0, ErrNotFound
}

func (f *fakeSystem) nameFromLUID(luid uint64) (string, error) {
	for name, l := range f.names {
		if l == luid {
			return name, nil
		}
	}
	return "", ErrNotFound
}

func (f *fakeSystem) addresses(uint64) ([]address, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.addrs), f.record("addresses")
}

func (f *fakeSystem) addAddress(_ uint64, p netip.Prefix) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("add " + p.String()); err != nil {
		return err
	}
	f.addrs = append(f.addrs, address{Prefix: p, Manual: true, DAD: dadTentative})
	return nil
}

func (f *fakeSystem) deleteAddress(_ uint64, a netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("delete " + a.String()); err != nil {
		return err
	}
	f.addrs = slices.DeleteFunc(f.addrs, func(x address) bool { return x.Prefix.Addr() == a })
	return nil
}

func (f *fakeSystem) addressState(_ uint64, a netip.Addr) (dadState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("state " + a.String()); err != nil {
		return dadInvalid, err
	}
	for _, x := range f.addrs {
		if x.Prefix.Addr() != a {
			continue
		}
		switch {
		case f.duplicate:
			return dadDuplicate, nil
		case f.tentative > 0:
			f.tentative--
			return dadTentative, nil
		}
		return dadPreferred, nil
	}
	return dadInvalid, nil
}

func (f *fakeSystem) changeIPInterface(_ uint64, family Family, change func(*ipInterface)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.ifaces[family]
	if !ok {
		return errFamilyNotBound
	}
	if err := f.record("interface " + family.String()); err != nil {
		return err
	}
	change(row)
	return nil
}

func (f *fakeSystem) isUp(uint64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up, f.record("up")
}

func (f *fakeSystem) setUp(up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up = up
}

func (f *fakeSystem) operations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ops)
}

func (f *fakeSystem) manual() []netip.Prefix {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []netip.Prefix
	for _, a := range f.addrs {
		out = append(out, a.Prefix)
	}
	slices.SortFunc(out, comparePrefix)
	return out
}

func testLink() Link { return Link{LUID: 0xABCD, Index: 42, Name: "Plaitway-test-0"} }

func TestFindByNameAndLUID(t *testing.T) {
	useFakeSystem(t)
	want := testLink()
	byName, err := FindByName("Plaitway-test-0")
	if err != nil || byName != want {
		t.Fatalf("FindByName = %+v, %v; want %+v", byName, err, want)
	}
	byLUID, err := FindByLUID(0xABCD)
	if err != nil || byLUID != want {
		t.Fatalf("FindByLUID = %+v, %v; want %+v", byLUID, err, want)
	}
	for _, name := range []string{"", "Plaitway-test-9"} {
		if _, err := FindByName(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("FindByName(%q) = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := FindByLUID(1); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindByLUID(1) = %v, want ErrNotFound", err)
	}
}

func TestSetAddressesAddsWaitsAndIsIdempotent(t *testing.T) {
	fake := useFakeSystem(t)
	fake.tentative = 3
	wanted := prefixes("10.7.0.2/24", "fd00:7::2/64")
	if err := testLink().SetAddresses(wanted); err != nil {
		t.Fatal(err)
	}
	if got := fake.manual(); !slices.Equal(got, wanted) {
		t.Fatalf("addresses = %v, want %v", got, wanted)
	}
	if fake.tentative != 0 {
		t.Errorf("returned while %d tentative polls were left", fake.tentative)
	}

	before := len(fake.operations())
	if err := testLink().SetAddresses(wanted); err != nil {
		t.Fatal(err)
	}
	for _, op := range fake.operations()[before:] {
		if op != "addresses" {
			t.Errorf("a second call with the same addresses did %q", op)
		}
	}
}

func TestSetAddressesReplacesOnlyWhatIsManual(t *testing.T) {
	fake := useFakeSystem(t)
	fake.addrs = []address{
		{Prefix: netip.MustParsePrefix("10.7.0.2/24"), Manual: true, DAD: dadPreferred},
		{Prefix: netip.MustParsePrefix("10.7.0.9/24"), Manual: true, DAD: dadPreferred},
		{Prefix: netip.MustParsePrefix("169.254.3.4/16"), Manual: false, DAD: dadPreferred},
		{Prefix: netip.MustParsePrefix("fe80::1/64"), Manual: true, DAD: dadPreferred},
		{Prefix: netip.MustParsePrefix("127.0.0.1/8"), Manual: true, DAD: dadPreferred},
	}
	// 10.7.0.2 changes its prefix length, 10.7.0.9 goes away, the rest stays.
	if err := testLink().SetAddresses(prefixes("10.7.0.2/32")); err != nil {
		t.Fatal(err)
	}
	want := prefixes("10.7.0.2/32", "127.0.0.1/8", "169.254.3.4/16", "fe80::1/64")
	if got := fake.manual(); !slices.Equal(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	if err := testLink().SetAddresses(nil); err != nil {
		t.Fatal(err)
	}
	want = prefixes("127.0.0.1/8", "169.254.3.4/16", "fe80::1/64")
	if got := fake.manual(); !slices.Equal(got, want) {
		t.Fatalf("after clearing: %v, want %v", got, want)
	}
}

func TestSetAddressesLeavesWhatTheSystemConfigured(t *testing.T) {
	fake := useFakeSystem(t)
	fake.addrs = []address{
		{Prefix: netip.MustParsePrefix("192.168.9.9/24"), Manual: false, DAD: dadPreferred},   // DHCP
		{Prefix: netip.MustParsePrefix("2001:db8:1::5/64"), Manual: false, DAD: dadPreferred}, // router advertisement
	}
	if err := testLink().SetAddresses(prefixes("10.7.0.2/24")); err != nil {
		t.Fatal(err)
	}
	want := prefixes("10.7.0.2/24", "192.168.9.9/24", "2001:db8:1::5/64")
	if got := fake.manual(); !slices.Equal(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	// An address that is wanted and already there is left alone, whoever made it.
	before := len(fake.operations())
	if err := testLink().SetAddresses(prefixes("10.7.0.2/24", "192.168.9.9/24")); err != nil {
		t.Fatal(err)
	}
	for _, op := range fake.operations()[before:] {
		if op != "addresses" {
			t.Errorf("wanting an address that the system configured did %q", op)
		}
	}
}

func TestSetAddressesFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name  string
		setup func(*fakeSystem)
		want  func(error) bool
	}{
		{"list fails", func(f *fakeSystem) { f.failOn["addresses"] = boom }, func(err error) bool { return errors.Is(err, boom) }},
		{"add fails", func(f *fakeSystem) { f.failOn["add 10.7.0.2/24"] = boom }, func(err error) bool { return errors.Is(err, boom) }},
		{"duplicate address", func(f *fakeSystem) { f.duplicate = true }, func(err error) bool { return err != nil }},
		{"tentative forever", func(f *fakeSystem) { f.tentative = 1 << 30 }, func(err error) bool { return err != nil }},
		{"state read fails", func(f *fakeSystem) { f.failOn["state 10.7.0.2"] = boom }, func(err error) bool { return errors.Is(err, boom) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := useFakeSystem(t)
			tt.setup(fake)
			err := testLink().SetAddresses(prefixes("10.7.0.2/24"))
			if !tt.want(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("delete fails", func(t *testing.T) {
		fake := useFakeSystem(t)
		fake.addrs = []address{{Prefix: netip.MustParsePrefix("10.7.0.9/24"), Manual: true, DAD: dadPreferred}}
		fake.failOn["delete 10.7.0.9"] = boom
		if err := testLink().SetAddresses(prefixes("10.7.0.2/24")); !errors.Is(err, boom) {
			t.Fatalf("error = %v", err)
		}
		if slices.Contains(fake.operations(), "add 10.7.0.2/24") {
			t.Error("added an address after a failed removal")
		}
	})
}

func TestNormalizePrefixes(t *testing.T) {
	tests := []struct {
		name string
		in   []netip.Prefix
		want []netip.Prefix
		bad  bool
	}{
		{"sorted without duplicates", prefixes("fd00::2/64", "10.0.0.2/24", "10.0.0.2/24"), prefixes("10.0.0.2/24", "fd00::2/64"), false},
		{"mapped IPv4", []netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.2/120")}, prefixes("10.0.0.2/24"), false},
		{"nothing", nil, []netip.Prefix{}, false},
		{"zero prefix", []netip.Prefix{{}}, nil, true},
		{"unspecified", prefixes("0.0.0.0/0"), nil, true},
		{"loopback", prefixes("127.0.0.2/8"), nil, true},
		{"multicast", prefixes("224.0.0.1/4"), nil, true},
		{"link local", prefixes("fe80::2/64"), nil, true},
		{"mapped with a short prefix", []netip.Prefix{netip.MustParsePrefix("::ffff:10.0.0.2/64")}, nil, true},
		{"same address two lengths", prefixes("10.0.0.2/24", "10.0.0.2/32"), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizePrefixes(tt.in)
			if (err != nil) != tt.bad {
				t.Fatalf("error = %v, want error: %v", err, tt.bad)
			}
			if !tt.bad && !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetMTU(t *testing.T) {
	fake := useFakeSystem(t)
	if err := testLink().SetMTU(1380, IPv4); err != nil {
		t.Fatal(err)
	}
	if fake.ifaces[IPv4].MTU != 1380 || fake.ifaces[IPv6].MTU != 1500 {
		t.Errorf("MTUs = %d / %d, want 1380 / 1500", fake.ifaces[IPv4].MTU, fake.ifaces[IPv6].MTU)
	}
	if err := testLink().SetMTU(1280, IPv6); err != nil || fake.ifaces[IPv6].MTU != 1280 {
		t.Errorf("IPv6 MTU = %d, %v", fake.ifaces[IPv6].MTU, err)
	}
	if err := testLink().SetMTU(0, IPv4); err == nil {
		t.Error("MTU 0 accepted")
	}
	if err := testLink().SetMTU(1400, Family(99)); err == nil {
		t.Error("unknown family accepted")
	}
	delete(fake.ifaces, IPv6)
	if err := testLink().SetMTU(1280, IPv6); !errors.Is(err, errFamilyNotBound) {
		t.Errorf("IPv6 without a stack: %v", err)
	}
}

func TestSetInterfaceMetric(t *testing.T) {
	fake := useFakeSystem(t)
	if err := testLink().SetInterfaceMetric(5); err != nil {
		t.Fatal(err)
	}
	for family, row := range fake.ifaces {
		if row.Metric != 5 || row.AutoMetric {
			t.Errorf("%s: metric %d auto %v, want 5 and manual", family, row.Metric, row.AutoMetric)
		}
	}
	if err := testLink().SetInterfaceMetric(0); err != nil {
		t.Fatal(err)
	}
	for family, row := range fake.ifaces {
		if !row.AutoMetric {
			t.Errorf("%s: metric 0 did not restore the automatic metric", family)
		}
	}

	delete(fake.ifaces, IPv6)
	if err := testLink().SetInterfaceMetric(7); err != nil || fake.ifaces[IPv4].Metric != 7 {
		t.Errorf("link without IPv6: %v, IPv4 metric %d", err, fake.ifaces[IPv4].Metric)
	}
	delete(fake.ifaces, IPv4)
	if err := testLink().SetInterfaceMetric(7); !errors.Is(err, errFamilyNotBound) {
		t.Errorf("link without any IP stack: %v", err)
	}

	boom := errors.New("boom")
	fake.ifaces[IPv4] = &ipInterface{}
	fake.failOn["interface IPv4"] = boom
	if err := testLink().SetInterfaceMetric(7); !errors.Is(err, boom) {
		t.Errorf("a failing family is hidden: %v", err)
	}
}

func TestWaitUp(t *testing.T) {
	fake := useFakeSystem(t)
	time.AfterFunc(20*time.Millisecond, func() { fake.setUp(true) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := testLink().WaitUp(ctx); err != nil {
		t.Fatalf("WaitUp: %v", err)
	}

	fake.setUp(false)
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShort()
	if err := testLink().WaitUp(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a link that stays down: %v, want the deadline", err)
	}

	boom := errors.New("boom")
	fake.failOn["up"] = boom
	if err := testLink().WaitUp(context.Background()); !errors.Is(err, boom) {
		t.Errorf("status error: %v", err)
	}
}

func TestFamilyString(t *testing.T) {
	if IPv4.String() != "IPv4" || IPv6.String() != "IPv6" || Family(3).String() != "address family 3" {
		t.Errorf("names: %s %s %s", IPv4, IPv6, Family(3))
	}
}
