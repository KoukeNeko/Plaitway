package linux

import (
	"math"
	"strings"
	"testing"
)

// ConfigureLink checks its arguments before it opens a socket, so these need
// neither a link nor privileges.
func TestConfigureLinkRefusesBadArguments(t *testing.T) {
	beyond := math.MaxInt32
	beyond++ // computed, because the constant does not fit an int on every platform
	tests := []struct {
		name  string
		addrs string
		mtu   int
		want  string
	}{
		{"negative MTU", "10.8.0.2/24", -1, "out of range"},
		{"MTU beyond 31 bits", "10.8.0.2/24", beyond, "out of range"},
		// The kernel removes the IPv6 addresses of a link when its MTU goes below 1280.
		{"MTU below the IPv6 minimum with an IPv6 address", "10.8.0.2/24 2001:db8:8::2/64", 1000, "IPv6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ConfigureLink("nosuch0", prefixes(tt.addrs), tt.mtu)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "nosuch0") {
				t.Errorf("ConfigureLink = %v, want an error naming the link and %q", err, tt.want)
			}
		})
	}
}
