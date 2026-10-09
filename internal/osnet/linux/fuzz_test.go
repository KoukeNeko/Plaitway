package linux

import (
	"net/netip"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// FuzzParseMessages feeds the parser arbitrary bytes. Whatever it accepts must
// not panic anywhere it is used afterwards, and the routes made of it must be
// ones the Reconciler can use and Delete can name.
func FuzzParseMessages(f *testing.F) {
	for _, set := range []map[string]string{routeMessages, linkMessages, addrMessages, errorMessages, goldenRequests} {
		for name := range set {
			f.Add(fixture(f, set, name))
		}
	}
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(append(fixture(f, routeMessages, "v4-multipath"), fixture(f, linkMessages, "lo")...))

	names := func(index uint32) string { return "if" + string(rune('0'+index%10)) }
	lookup := func(index uint32) (link, bool) {
		return link{Interface: osnet.Interface{Name: names(index), Index: int(index)}, Uplink: index%2 == 0}, index%5 != 0
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		// The unguarded parser: a panic is a bug to find, not to recover from.
		msgs, err := parseMessagesUnguarded(in)
		if err != nil {
			return
		}
		for _, m := range msgs {
			relevantMessage(m, lookup)
			if m.Link != nil {
				linkFromMessage(*m.Link)
			}
			if m.Addr != nil {
				if p, ok := m.Addr.prefix(); ok && (!p.IsValid() || p.Addr().Is4In6()) {
					t.Fatalf("address message %+v gives prefix %v", *m.Addr, p)
				}
			}
			if m.Route == nil {
				continue
			}
			for _, r := range routesFromMessage(*m.Route, names) {
				checkRoute(t, r)
				for _, typ := range []uint16{rtmNewRoute, rtmDelRoute} {
					req, err := newRouteRequest(typ, r, func(string) (uint32, error) { return r.IfIndex, nil })
					if err != nil {
						continue
					}
					// What we send is what we parse: the same shape in both directions.
					sent, err := parseMessagesUnguarded(req.marshal(1))
					if err != nil || len(sent) != 1 || sent[0].Route == nil {
						t.Fatalf("request %+v for %+v does not parse back: %v", req, r, err)
					}
					if back, ok := sent[0].Route.prefix(); !ok || back != r.Dst {
						t.Fatalf("request for %v parses back as %v", r.Dst, back)
					}
				}
			}
		}
	})
}

// checkRoute fails the test for a route the rest of Plaitway could trip over.
func checkRoute(t *testing.T, r osnet.Route) {
	t.Helper()
	if !r.Dst.IsValid() || r.Dst != r.Dst.Masked() || r.Dst.Addr().Is4In6() {
		t.Fatalf("destination %v of %+v is not a masked prefix", r.Dst, r)
	}
	if r.Gateway.IsValid() {
		if r.Gateway.Is4() != r.Dst.Addr().Is4() {
			t.Fatalf("gateway of another family than the destination: %+v", r)
		}
		if r.Gateway.Zone() != "" && r.Gateway.Zone() != r.Iface {
			t.Fatalf("zone of the gateway is not the interface: %+v", r)
		}
	}
	if !r.Blackhole && r.IfIndex == 0 && !r.Gateway.IsValid() {
		t.Fatalf("a route that leads nowhere: %+v", r)
	}
	if r.Dst.Addr() == (netip.Addr{}) {
		t.Fatalf("no destination: %+v", r)
	}
}
