package wg

import (
	"math/bits"
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"
)

// publicV4 is 0.0.0.0/0 less the private IPv4 ranges (10.0.0.0/8, 172.16.0.0/12,
// 192.168.0.0/16 and 169.254.0.0/16), worked out one first octet at a time.
var publicV4 = pfx(
	"0.0.0.0/5", "8.0.0.0/7", "11.0.0.0/8", "12.0.0.0/6", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2",
	"128.0.0.0/3", "160.0.0.0/5", "168.0.0.0/8",
	"169.0.0.0/9", "169.128.0.0/10", "169.192.0.0/11", "169.224.0.0/12", "169.240.0.0/13", "169.248.0.0/14",
	"169.252.0.0/15", "169.255.0.0/16", "170.0.0.0/7",
	"172.0.0.0/12", "172.32.0.0/11", "172.64.0.0/10", "172.128.0.0/9", "173.0.0.0/8", "174.0.0.0/7", "176.0.0.0/4",
	"192.0.0.0/9", "192.128.0.0/11", "192.160.0.0/13", "192.169.0.0/16", "192.170.0.0/15", "192.172.0.0/14",
	"192.176.0.0/12", "192.192.0.0/10", "193.0.0.0/8", "194.0.0.0/7", "196.0.0.0/6", "200.0.0.0/5", "208.0.0.0/4",
	"224.0.0.0/3",
)

// publicV6 is ::/0 less fc00::/7 and fe80::/10.
var publicV6 = pfx("::/1", "8000::/2", "c000::/3", "e000::/4", "f000::/5", "f800::/6", "fe00::/9", "fec0::/10", "ff00::/8")

func TestSubtractPrefixes(t *testing.T) {
	tests := []struct {
		name     string
		from     []netip.Prefix
		excluded []netip.Prefix
		want     []netip.Prefix
	}{
		{"all IPv4 less the private ranges", pfx("0.0.0.0/0"), privateRanges, publicV4},
		{"all IPv6 less the private ranges", pfx("::/0"), privateRanges, publicV6},
		{"both families, IPv4 first", pfx("::/0", "0.0.0.0/0"), privateRanges, slices.Concat(publicV4, publicV6)},
		{"a private subnet disappears", pfx("10.6.0.0/24"), privateRanges, nil},
		{"a peer address inside 192.168.0.0/16 disappears", pfx("192.168.1.5/32"), privateRanges, nil},
		{"IPv6 private addresses disappear", pfx("fd00::2/128", "fe80::1/128", "fc00::/7"), privateRanges, nil},
		{"the private range itself disappears", pfx("172.16.0.0/12"), privateRanges, nil},
		{"a public prefix stays", pfx("203.0.113.0/24", "2001:db8::/32"), privateRanges, pfx("203.0.113.0/24", "2001:db8::/32")},
		{"a prefix around a private range is split", pfx("192.168.0.0/15"), privateRanges, pfx("192.169.0.0/16")},
		{"a prefix that starts in a private range", pfx("10.0.0.0/7"), privateRanges, pfx("11.0.0.0/8")},
		{"a prefix next to a private range stays whole", pfx("8.0.0.0/7"), privateRanges, pfx("8.0.0.0/7")},
		{"a /8 around a /12", pfx("172.0.0.0/8"), privateRanges, pfx("172.0.0.0/12", "172.32.0.0/11", "172.64.0.0/10", "172.128.0.0/9")},
		{"a range of another family is no exclusion", pfx("::/0"), pfx("10.0.0.0/8"), pfx("::/0")},
		{"nothing to take away", pfx("0.0.0.0/0"), nil, pfx("0.0.0.0/0")},
		{"nothing to take from", nil, privateRanges, nil},
		{"halves become the whole", pfx("128.0.0.0/1", "0.0.0.0/1"), nil, pfx("0.0.0.0/0")},
		{"overlaps and duplicates are one", pfx("203.0.113.0/24", "203.0.113.128/25", "203.0.113.0/24"), nil, pfx("203.0.113.0/24")},
		{"host bits are ignored", pfx("203.0.113.7/24"), nil, pfx("203.0.113.0/24")},
		{"sorted by address, IPv4 first", pfx("2001:db8::/32", "203.0.113.0/24", "198.51.100.0/24"), nil, pfx("198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32")},
		{"neighbours that are not siblings stay apart", pfx("203.0.113.0/25", "203.0.113.128/26"), nil, pfx("203.0.113.0/25", "203.0.113.128/26")},
		{"a piece next to another prefix stays a piece", pfx("192.168.0.0/15", "192.170.0.0/15"), privateRanges, pfx("192.169.0.0/16", "192.170.0.0/15")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := slices.Clone(tt.from)
			got := subtractPrefixes(tt.from, tt.excluded)
			if !slices.Equal(got, tt.want) {
				t.Errorf("subtractPrefixes(%v, %v) = %v, want %v", tt.from, tt.excluded, got, tt.want)
			}
			if !slices.Equal(tt.from, before) {
				t.Errorf("the input was changed: %v, was %v", tt.from, before)
			}
		})
	}
}

// windowAddr is the i-th address of a window of 256 addresses.
func windowAddr(window netip.Prefix, i int) netip.Addr {
	raw := window.Addr().AsSlice()
	raw[len(raw)-1] = byte(i)
	addr, _ := netip.AddrFromSlice(raw)
	return addr
}

// bruteForce lists address by address which addresses of the window are in
// from and not in excluded, and covers them with the fewest prefixes: from the
// lowest address up, the largest aligned block that is entirely in.
func bruteForce(window netip.Prefix, from, excluded []netip.Prefix) []netip.Prefix {
	contained := func(prefixes []netip.Prefix, addr netip.Addr) bool {
		return slices.ContainsFunc(prefixes, func(p netip.Prefix) bool { return p.Contains(addr) })
	}
	var in [256]bool
	for i := range in {
		addr := windowAddr(window, i)
		in[i] = contained(from, addr) && !contained(excluded, addr)
	}
	var out []netip.Prefix
	for i := 0; i < len(in); {
		if !in[i] {
			i++
			continue
		}
		size := len(in)
		for i%size != 0 || slices.Contains(in[i:i+size], false) {
			size /= 2
		}
		out = append(out, netip.PrefixFrom(windowAddr(window, i), window.Bits()+8-bits.TrailingZeros(uint(size))))
		i += size
	}
	return out
}

func TestSubtractPrefixesMatchesABruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for _, window := range pfx("192.0.2.0/24", "2001:db8::/120") {
		randomPrefixes := func(most int) []netip.Prefix {
			var prefixes []netip.Prefix
			for range rng.IntN(most + 1) {
				// Not masked: the host bits must not matter.
				prefixes = append(prefixes, netip.PrefixFrom(windowAddr(window, rng.IntN(256)), window.Bits()+rng.IntN(9)))
			}
			return prefixes
		}
		for range 5000 {
			from, excluded := randomPrefixes(6), randomPrefixes(4)
			want := bruteForce(window, from, excluded)
			if got := subtractPrefixes(from, excluded); !slices.Equal(got, want) {
				t.Fatalf("subtractPrefixes(%v, %v) = %v, want %v", from, excluded, got, want)
			}
		}
	}
}
