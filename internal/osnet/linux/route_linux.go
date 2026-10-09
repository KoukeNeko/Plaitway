package linux

import (
	"fmt"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// routeTable reads the main routing table over rtnetlink and changes it with
// route messages.
type routeTable struct{}

// NewRouteTable returns the routing table of this machine. Writing it needs
// CAP_NET_ADMIN.
func NewRouteTable() osnet.RouteTable { return routeTable{} }

func (routeTable) Dump() ([]osnet.Route, error) {
	conn, err := openNetlink(0)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	routeMsgs, err := conn.dump(rtmGetRoute)
	if err != nil {
		return nil, fmt.Errorf("read routing table: %w", err)
	}
	// The names are read after the routes: an interface that appeared in between
	// is named, one that vanished takes its routes with it.
	links, err := conn.links(false)
	if err != nil {
		return nil, err
	}
	names := make(map[uint32]string, len(links))
	for _, l := range links {
		names[uint32(l.Index)] = l.Name
	}
	ifaceName := func(index uint32) string { return names[index] }
	var routes []osnet.Route
	for _, m := range routeMsgs {
		if m.Route != nil {
			routes = append(routes, routesFromMessage(*m.Route, ifaceName)...)
		}
	}
	sortRoutes(routes)
	return routes, nil
}

func (routeTable) Add(r osnet.Route) error    { return writeRoute(opAdd, rtmNewRoute, r) }
func (routeTable) Delete(r osnet.Route) error { return writeRoute(opDelete, rtmDelRoute, r) }

// writeRoute sends one request. The kernel answers a refusal with an errno,
// which is what "ip route" turns into a message and exit status 2.
func writeRoute(op string, typ uint16, r osnet.Route) error {
	conn, err := openNetlink(0)
	if err != nil {
		return fmt.Errorf("%s route %s: %w", op, r.Dst, err)
	}
	defer conn.close()
	req, err := newRouteRequest(typ, r, conn.interfaceIndex)
	if err != nil {
		return routeError(op, r.Dst, err)
	}
	if err := conn.request(req.marshal); err != nil {
		return routeError(op, req.Dst, err)
	}
	return nil
}
