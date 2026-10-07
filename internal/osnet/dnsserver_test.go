package osnet

import (
	"net/netip"
	"testing"
)

func TestValidDNSServer(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"10.6.0.1", true},
		{"2001:db8::53", true},
		{"::ffff:10.6.0.2", true}, // the adapter writes it as 10.6.0.2
		{"127.0.0.1", true},
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.251", false},
		{"ff02::fb", false},
		{"fe80::1%en0", false},
		{"", false},
	}
	for _, tt := range tests {
		var addr netip.Addr
		if tt.addr != "" {
			addr = netip.MustParseAddr(tt.addr)
		}
		if got := ValidDNSServer(addr); got != tt.want {
			t.Errorf("ValidDNSServer(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}
