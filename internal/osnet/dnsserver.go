package osnet

import "net/netip"

// ValidDNSServer reports whether a can be written into a resolver entry: a
// unicast address without a zone. It is the one rule for what a DNSConfigurator
// refuses, so that the Reconciler can leave such a nameserver out of an entry
// instead of passing on one the adapter will refuse on every attempt.
func ValidDNSServer(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast()
}
