package wg

import (
	"context"
	"net/netip"
)

// Interfaces applies the profile's addresses and MTU to a tunnel interface the
// daemon has just created. The interface goes away when its device is closed,
// so there is nothing to undo.
type Interfaces interface {
	Configure(ctx context.Context, name string, addrs []netip.Prefix, mtu int) error
}
