package macos

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"syscall"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// routeTable reads the routing table from the kernel's RIB and changes it with
// routing socket messages.
type routeTable struct {
	seq atomic.Int32
}

// NewRouteTable returns the routing table of this Mac. Writing it needs root.
func NewRouteTable() osnet.RouteTable { return &routeTable{} }

func (t *routeTable) Dump() ([]osnet.Route, error) {
	var msgs []rtMessage
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		rib, err := route.FetchRIB(family, route.RIBTypeRoute, 0)
		if err != nil {
			return nil, fmt.Errorf("read routing table of family %d: %w", family, err)
		}
		parsed, err := parseMessages(rib)
		if err != nil {
			return nil, fmt.Errorf("parse routing table of family %d: %w", family, err)
		}
		msgs = append(msgs, parsed...)
	}
	// The names are read after the routes: an interface that appeared in between
	// is named, one that vanished takes its routes with it.
	names, err := interfaceNames()
	if err != nil {
		return nil, err
	}
	ifaceName := func(index int) string { return names[index] }
	var routes []osnet.Route
	for _, m := range msgs {
		if m.Type != rtmGet {
			continue
		}
		if r, ok := routeFromMessage(m, ifaceName); ok {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func (t *routeTable) Add(r osnet.Route) error    { return t.write("add", rtmAdd, r) }
func (t *routeTable) Delete(r osnet.Route) error { return t.write("delete", rtmDelete, r) }

// write sends one request. The kernel reports a failure as the errno of the
// write, which is what route(8) turns into a message and exit status 0.
func (t *routeTable) write(op string, typ int, r osnet.Route) error {
	req, err := newRouteRequest(typ, r, interfaceIndex)
	if err != nil {
		return fmt.Errorf("%s route %s: %w", op, r.Dst, err)
	}
	req.PID, req.Seq = int32(os.Getpid()), t.seq.Add(1)
	sock, err := openRouteSocket()
	if err != nil {
		return fmt.Errorf("%s route %s: %w", op, r.Dst, err)
	}
	defer sock.Close()
	if _, err := sock.Write(req.marshal()); err != nil {
		return routeError(op, req.Dst, err)
	}
	return nil
}

func interfaceIndex(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("interface %s: %w", name, err)
	}
	return iface.Index, nil
}

// openRouteSocket opens a PF_ROUTE socket. As a *os.File it is polled by the
// runtime, so closing it ends a blocked Read.
func openRouteSocket() (*os.File, error) {
	syscall.ForkLock.RLock()
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("open routing socket: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("open routing socket: %w", err)
	}
	return os.NewFile(uintptr(fd), "route"), nil
}
