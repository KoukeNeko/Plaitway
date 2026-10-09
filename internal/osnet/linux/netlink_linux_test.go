package linux

import (
	"errors"
	"syscall"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// The replies of a real kernel to requests it refused, run through the error
// handling of the route table.
func TestKernelRefusalsBecomeOsnetErrors(t *testing.T) {
	dst := prefix("198.51.100.0/24")
	refused := newNetlinkError(fixtureMessage(t, errorMessages, "add-bad-gateway"))
	if !errors.Is(refused, syscall.ENETUNREACH) || refused.ExtAck != "Nexthop has invalid gateway" {
		t.Fatalf("refused add: %#v", refused)
	}
	if got := routeError(opAdd, dst, refused); !errors.Is(got, osnet.ErrUnreachable) {
		t.Errorf("add through a gateway on no connected network = %v, want ErrUnreachable", got)
	}
	absent := newNetlinkError(fixtureMessage(t, errorMessages, "del-absent"))
	if got := routeError(opDelete, dst, absent); !errors.Is(got, osnet.ErrNotFound) {
		t.Errorf("delete of an absent route = %v, want ErrNotFound", got)
	}
	// A refusal without a mapping keeps what the kernel said.
	inval := &netlinkError{Errno: syscall.EINVAL, ExtAck: "Invalid prefix for given prefix length"}
	got := routeError(opAdd, dst, inval)
	if !errors.Is(got, syscall.EINVAL) || got.Error() != "add route 198.51.100.0/24: invalid argument (Invalid prefix for given prefix length)" {
		t.Errorf("unmapped refusal = %v", got)
	}
}
