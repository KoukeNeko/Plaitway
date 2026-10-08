package reconciler

import (
	"fmt"
	"slices"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// maxOwnerLen bounds owner ids; the bound of interface names depends on the
// platform, see RouteKeying.
const maxOwnerLen = 64

// validateIntent checks the identifiers of an intent that an engine announced:
// the owner ends up in resolver keys and journal records, the interface name in
// route commands. Everything else in an intent is data from profiles and
// servers; Compute decides what to do with what it cannot use, and a bad
// route or domain must not take a whole tunnel down.
func validateIntent(keying RouteKeying, in tunnel.Intent) error {
	if !validName(string(in.Owner), maxOwnerLen) {
		return fmt.Errorf("invalid owner %q", in.Owner)
	}
	if carriesRoutes(in.State) && !validName(in.Iface, keying.maxIfaceName()) {
		return fmt.Errorf("owner %s: invalid interface %q", in.Owner, in.Iface)
	}
	return nil
}

// validName accepts what is safe in an interface name, a file name and a
// resolver key. It cannot start with a punctuation mark, so that it is never
// taken for an option when it ends up on a command line.
func validName(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '-' || c == '_' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// cloneIntent copies the slices, so the engine cannot change a stored intent.
func cloneIntent(in tunnel.Intent) tunnel.Intent {
	in.Endpoints = slices.Clone(in.Endpoints)
	in.Routes = slices.Clone(in.Routes)
	in.DNS = slices.Clone(in.DNS)
	for i, d := range in.DNS {
		in.DNS[i] = tunnel.DNSIntent{Servers: slices.Clone(d.Servers), MatchDomains: slices.Clone(d.MatchDomains)}
	}
	return in
}
