// Package tunnel holds what the VPN engines, the Reconciler and the daemon's
// profile manager exchange. It has types and interfaces only.
package tunnel

import (
	"context"
	"net/netip"
	"time"

	"github.com/KoukeNeko/Plaitway/internal/osnet"
)

// OwnerID identifies one profile; it is the profile's id.
type OwnerID string

type Kind uint8

const (
	KindOpenVPN Kind = iota + 1
	KindWireGuard
)

type State uint8

const (
	StateDisconnected State = iota
	StateConnecting
	StateAwaitingCredentials
	StateUp
	// StateReconnecting: the underlay network changed and the tunnel is being
	// re-established; the tunnel interface and its routes are kept.
	StateReconnecting
	StateDisconnecting
	StateFailed
)

// Mode is the user's choice about sending all traffic through a tunnel.
type Mode uint8

const (
	ModeAuto  Mode = iota // as the profile itself says
	ModeFull              // all traffic through the tunnel
	ModeSplit             // only the routes the profile names; a default route is dropped
)

// Role is what a tunnel asks the Reconciler for.
type Role uint8

const (
	RoleSplit Role = iota
	RoleFull
)

// Spec is everything an engine needs to run one profile.
type Spec struct {
	Owner OwnerID
	Name  string
	// Content is the stored profile text, already accepted by the engine's Parse.
	Content  []byte
	Mode     Mode
	Priority int // lower wins overlapping routes and DNS domains
	// ExcludePrivateIPs is the WireGuard option that keeps the private ranges
	// out of AllowedIPs and so out of the tunnel.
	ExcludePrivateIPs bool
}

type Endpoint struct {
	Host     string
	Port     uint16
	Protocol string // "udp" or "tcp" for OpenVPN; empty for WireGuard
}

// Summary is what Parse learns from a profile without running it.
type Summary struct {
	Endpoints             []Endpoint
	Routes                []netip.Prefix // prefixes the profile names itself
	RedirectsDefaultRoute bool
	RequiresCredentials   bool
	DNSServers            []netip.Addr
	Addresses             []netip.Prefix // addresses for the tunnel interface
	// PublicKey is the WireGuard public key derived from the private key; empty for OpenVPN.
	PublicKey string
}

// Warning reports a directive Parse stripped or ignored.
type Warning struct {
	Line      int
	Directive string
	Message   string
}

type DNSIntent struct {
	Servers []netip.Addr
	// MatchDomains are the domains these servers answer; "." means everything.
	// Empty means the same as ".": only the owner of the default routes gets a
	// catch-all, so a split tunnel that pushes servers without domains gets no
	// DNS entry.
	MatchDomains []string
}

// Intent is the complete, replace-all statement of what one tunnel wants from
// the host network. Announcing again replaces the previous intent of that owner.
type Intent struct {
	Owner OwnerID
	// State: StateUp and StateReconnecting contribute routes and DNS (a
	// reconnecting tunnel keeps its interface, and removing its routes would
	// leak traffic); StateConnecting contributes only its Endpoints.
	State    State
	Iface    string
	Role     Role
	Priority int
	UpSince  time.Time
	// Endpoints are the addresses the tunnel's own traffic goes to. The
	// Reconciler keeps them reachable outside the tunnel when a route would
	// otherwise capture them.
	Endpoints []netip.Addr
	// Routes are the prefixes to send through Iface. A default route is
	// written as 0.0.0.0/0 or ::/0 and the Reconciler splits it into halves.
	Routes []netip.Prefix
	DNS    []DNSIntent
}

type CredentialKind uint8

const (
	CredentialNone CredentialKind = iota
	CredentialUserPassword
	CredentialKeyPassphrase
)

type Stats struct {
	RxBytes, TxBytes uint64
}

// Status is a snapshot of one engine, for the UI. Routes and DNS state come
// from the Reconciler, not from here.
type Status struct {
	State State
	// Err describes the last failure; empty when none.
	Err       string
	Iface     string
	Addresses []netip.Prefix
	Remote    string
	Since     time.Time
	Stats     Stats
	// NeedsCredentials is set while State is StateAwaitingCredentials.
	NeedsCredentials CredentialKind
	Warnings         []string
}

type LogLevel uint8

const (
	LogDebug LogLevel = iota + 1
	LogInfo
	LogWarn
	LogError
)

// Network is the Reconciler as the engines see it.
type Network interface {
	// Announce replaces owner's intent and returns once the Reconciler has
	// applied it. An engine announces its endpoints in StateConnecting and
	// waits for this to return before it starts connecting.
	Announce(Intent) error
	// Withdraw removes everything owner announced and installed.
	Withdraw(OwnerID)
}

// Deps are the services the daemon gives an engine.
type Deps struct {
	Network Network
	// Net reads the physical network (default route, interfaces).
	Net osnet.NetMonitor
	// Log receives the engine's log lines; it must not block.
	Log func(level LogLevel, text string)
}

// Engine runs one profile.
//
// Start returns as soon as the tunnel is being brought up. Status delivers
// snapshots; a slow reader may miss intermediate ones but always sees the
// latest. Stop is idempotent. When it returns the tunnel interface and any
// child process are gone, the owner has been withdrawn from the Network, and the
// Status channel is closed.
type Engine interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Status() <-chan Status
	// ProvideCredentials answers a StateAwaitingCredentials request.
	ProvideCredentials(kind CredentialKind, username, password string) error
	// Rebind re-establishes the underlay connection after the physical network
	// changed. The Reconciler calls it after the routes have been fixed.
	Rebind()
}

// Parsed is the result of validating an untrusted profile file. Profiles are
// untrusted input to a root daemon: a Parse that accepts a file must have
// removed or rejected everything that could run programs or read other files.
type Parsed struct {
	Summary  Summary
	Warnings []Warning
	// Content is what gets stored: the input with rejected directives removed.
	Content []byte
	// SuggestedName is a name found inside the profile; empty when there is none.
	SuggestedName string
}

// EngineInfo says whether a backend can run on this machine.
type EngineInfo struct {
	Available bool
	Version   string
	Detail    string // why it is unavailable; empty otherwise
}

// Backend is one VPN protocol implementation.
type Backend struct {
	Kind Kind
	// Parse validates untrusted content. It returns an error naming the
	// offending line when the profile has to be rejected outright.
	Parse func(content []byte) (Parsed, error)
	// New creates an engine for a stored profile without starting it.
	New func(spec Spec, deps Deps) (Engine, error)
	// Probe reports whether the backend can run here.
	Probe func() EngineInfo
}

type RouteState uint8

const (
	RoutePending RouteState = iota
	RouteInstalled
	RouteShadowed // another owner with higher priority holds the prefix
	RouteBlocked  // conflicts with a directly attached local subnet
	RouteFailed
)

type RouteKind uint8

const (
	RouteTunnel      RouteKind = iota // bound to the tunnel interface
	RouteBypass                       // keeps a tunnel endpoint reachable outside the tunnel
	RouteDefaultHalf                  // half of a split default route
)

type RouteReport struct {
	Prefix     netip.Prefix
	Owner      OwnerID
	Kind       RouteKind
	Via        string // interface name or gateway
	State      RouteState
	Detail     string  // why it is not installed; empty when installed
	ShadowedBy OwnerID // set when State is RouteShadowed
}

type DNSReport struct {
	Owner        OwnerID
	Servers      []netip.Addr
	MatchDomains []string
	State        RouteState
	Detail       string
}

// StaleRoute is a route that no longer matches the network it was added for.
type StaleRoute struct {
	Key    string // stable identifier, passed to RemoveStale
	Route  osnet.Route
	Reason string
	// Owned is true when Plaitway's journal says it installed the route; only
	// those are removed automatically.
	Owned bool
}

type JournalRecord struct {
	Time  time.Time
	Owner OwnerID
	Kind  string // route, resolver
	Key   string
	State string // pending, applied, removed
}

// Report is what the Reconciler currently believes and has done.
type Report struct {
	Net             osnet.NetState
	LastChange      osnet.Change
	Routes          []RouteReport
	DNS             []DNSReport
	Stale           []StaleRoute
	ResolverEntries []string
	Journal         []JournalRecord // most recent last, bounded
}

// Reconciler is the only component that changes the host's routes and DNS. The
// daemon runs one; engines see it as a Network.
type Reconciler interface {
	Network
	// Run processes network events until ctx ends, then removes everything it owns.
	Run(ctx context.Context) error
	Report() Report
	// Changed signals, coalesced, that Report would now return something new.
	Changed() <-chan struct{}
	// Resync re-reads the network and rebuilds everything it owns from scratch.
	Resync() error
	// RemoveStale deletes one route returned in Report.Stale.
	RemoveStale(key string) error
	// SetRebind registers what to call after routes were repaired following a
	// change of the underlay network (the daemon asks every engine to Rebind).
	SetRebind(func())
}
