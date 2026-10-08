// Package windows implements the osnet interfaces on Windows with the IP Helper
// API and the registry: NewRouteTable, NewNetMonitor and NewDNS. The files that
// call the system end in _windows.go; the logic on top of them (error mapping,
// default route choice, link kinds, debouncing, DNS rule encoding) has no build
// tag, so it is tested on every OS.
//
// # Routes
//
// The table is read with GetIpForwardTable2 and written with
// CreateIpForwardEntry2 and DeleteIpForwardEntry2, in the active store only, so
// that no route survives a reboot. Windows keys a route by destination,
// interface and next hop: several routes can share a prefix, which is why
// osnet.Route has IfIndex and Metric, and why Delete without an interface
// looks the route up in the table and refuses to guess between several. The
// metric is an attribute, not part of the key (documented by Microsoft, and
// confirmed by TestRootRouteKeyIgnoresTheMetric).
//
// Static is the route protocol MIB_IPPROTO_NETMGMT, which every route that is
// not derived from an address has: an administrator's, a program's, DHCP's and
// the default route a router advertisement gave. Flags holds the origin in the
// upper half and the protocol in the lower half.
//
// Windows has no blackhole route. A blackhole is a route whose next hop is the
// loopback address on the loopback pseudo-interface, which is where the stack
// drops what it cannot deliver; Dump reads such a route back as Blackhole. The
// IPv4 form is the workaround that Windows administrators use. The IPv6 form
// (next hop ::1) follows from it; TestRootBlackholeRoutes checks both.
//
// # Network state
//
// Snapshot reads every interface (GetIfTable2Ex without the NDIS filter
// pseudo-interfaces that the table lists next to each adapter), their unicast
// addresses and interface metrics, and the routing table. The default route is
// the one of the lowest effective metric (route metric plus interface metric)
// among the interfaces that are up and a way out (see below), so a VPN's
// default route never counts, whatever its metric.
//
// osnet.Interface.Metric is the interface metric of GetIpInterfaceEntry, the
// value that Windows adds to the metric of every route through the interface.
// For an interface with an automatic metric that is the value the system
// computed (from the link speed), not a stored zero;
// TestRealSnapshotInterfaceMetricsAreTheEffectiveOnes compares it with what
// netsh prints. It is the IPv4 metric, the IPv6 one for an interface that has
// no IPv4 stack, and 0 for an interface that is down or has no IP stack (a
// virtual switch extension,
// Wi-Fi Direct). The epoch ignores it: an automatic metric moves with the
// speed of the link, and a change that matters shows in the default route.
//
// An interface is a tunnel when its type is IF_TYPE_TUNNEL, IF_TYPE_PPP or
// IF_TYPE_PROP_VIRTUAL (53: the OpenVPN TAP and data channel offload drivers
// report this type), or when its driver description names a known VPN or
// tunnel driver. Evidence, from the PC this was written on: Hyper-V adapters
// and the Bluetooth personal area network are type 6 (Ethernet) with
// HardwareInterface false; WAN Miniports are types 6, 23 and 131; USB
// tethering ("UsbNcm Host Device") is hardware.
//
// An interface is a way out (isPhysical: the default route of the PC, and the
// gateway that the routes to a VPN's server go through) when it is not a
// tunnel or the loopback and either is hardware or is the adapter of a Hyper-V
// external switch. The hardware flag is required because the list of known
// tunnels is only a list: a VPN adapter that reports Ethernet, is virtual and
// has an unknown name would otherwise count, and the routes to another VPN's
// server would go through its gateway. The known names stay as a second guard
// for the VPN drivers that claim to be hardware (Cisco AnyConnect). The
// consequence is that a PC whose only link is a virtual one (Bluetooth
// personal area network, an unknown software adapter) has no default: the
// bypass routes of a full tunnel then say "no network", the on-demand rules see
// no network, and the diagnostics show no default gateway, until a hardware
// link comes up. That is the safe side of the error: no route goes through
// a gateway that might belong to a VPN. The Hyper-V exception is by driver
// description, not by the existence of a physical adapter behind the switch:
// an external switch's adapter holds the addresses and the default route, and
// an internal or NAT switch (Default Switch, WSL) never has a default route, so
// the exception cannot make one a way out. Subnets are different: the on-link
// subnet of any interface that is not a tunnel or the loopback is a local
// network (isLocalNetwork), virtual switches and unknown virtual adapters
// included, so that a VPN route into it is blocked and not routed away.
//
// An interface is Wi-Fi or Ethernet for the on-demand rules only when it is a
// way out by hardware and its description does not say that it is virtual, a
// Bluetooth link or a tethered phone.
//
// Events registers three change notifications (NotifyRouteChange2,
// NotifyIpInterfaceChange, NotifyUnicastIpAddressChange) and a power
// notification (PowerRegisterSuspendResumeNotification with a callback, which
// works in a service and in a console program, where a window message would
// not). Notifications are merged into debounced Changes, and a failed
// registration is retried. Every registration is cancelled when ctx ends.
//
// # DNS
//
// The question is how to say "these servers answer these domains, and "." means
// everything" on Windows, find the entries again after a crash, and remove only
// our own. Three designs were weighed.
//
//   - Per-interface DNS (SetInterfaceDnsSettings) sets the servers and a search
//     list of one adapter. It cannot say "only these domains", and the DNS
//     Client still sends a query that no rule matches to the servers of every
//     adapter (smart multi-homed name resolution), so a full tunnel leaks to the
//     physical network's resolver. DNSEntry also names no interface, so
//     the configurator would have to infer one from the route table, which makes
//     DNS depend on routes the Reconciler owns.
//   - A combination (NRPT for domains, per-interface for the catch-all) has both
//     problems on the catch-all side and two places to clean up.
//   - NRPT rules (the Name Resolution Policy Table) send the names a rule
//     matches to the rule's servers and nowhere else, whatever the interfaces
//     say. "." matches every name, a more specific rule beats it, and nothing in
//     a rule refers to an adapter, so rules need no cleanup when an adapter goes.
//
// NRPT is the choice. Evidence: dnsrslvr.dll, the DNS Client service, contains
// the strings Parameters\DnsPolicyConfig, SOFTWARE\Policies\Microsoft\Windows
// NT\DNSClient, GenericDNSServers and ConfigOptions, which is the layout
// Add-DnsClientNrptRule writes; the PC this was written on has no rule, so the
// layout is taken from that cmdlet's documented output and from other programs
// that write the same key.
//
// A rule is the key HKLM\SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\
// DnsPolicyConfig\Plaitway-<hash>-<generation>-<n> with these values:
//
//	Name               REG_MULTI_SZ  ".example.com", "example.com" (suffix, exact), or "."
//	GenericDNSServers  REG_SZ        "10.6.0.1;fd00::1"
//	ConfigOptions      REG_DWORD     8 (the servers are GenericDNSServers)
//	Version            REG_DWORD     2
//	DisplayName        REG_SZ        "Plaitway"
//	Comment            REG_SZ        "Plaitway order=<n> owner=<owner>"
//
// A domain is written as both namespaces, the suffix and the exact name, so that
// it means itself and everything below it, as on macOS. The marker is the key
// name prefix "Plaitway-"; the owner is hashed (SHA-256, 16 hex digits) into the
// key name, so long names and odd characters need no escaping, and is stored
// verbatim in Comment, from which it is recoverable. Owned lists the keys; Remove
// accepts an owner or a key and removes every rule of that owner. Order is kept
// in Comment: the Reconciler gives each domain to one owner, so rules never
// compete, and NRPT has no order of its own.
//
// Apply writes the new rules under a new generation (Name last, so that a rule
// becomes visible only when it is complete), then removes the old generation: a
// resolver always finds a complete set. An Apply that changes nothing writes
// nothing. Flush calls DnsFlushResolverCache.
//
// After a change the DNS Client is told to reread its policy with
// RefreshPolicyEx, as other programs that write the NRPT do. The call is
// PROVISIONAL, pending TestRootDNSRegistryWriteWithoutPolicyRefresh, which has
// not been run on an elevated shell: if the DNS Client picks a rule up from the
// registry promptly without it, the call goes (refreshPolicyAfterChange in
// dns.go is the one line that switches it off). It forces a machine-wide group
// policy refresh, which is slow on a PC in a domain, and the Reconciler holds
// its lock for the duration of an Apply, so the call is bounded: it runs in a
// goroutine of its own, the pass waits for it five seconds at most and logs
// when it takes longer, and while one has not returned no second one is started.
// A failure of the call is logged, not returned.
//
// When a group policy or DirectAccess delivers an NRPT (any subkey of
// HKLM\SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig),
// Windows ignores the local rules. Apply then returns ErrGroupPolicyNRPT
// without writing, so that the Reconciler reports DNS as not in effect instead
// of installed; Remove and an Apply with no entries work as before, to take out
// the rules written before the policy arrived. A policy branch that cannot be
// read does not stop the rules from being written.
//
// Not confirmed without an elevated process, and checked by the rootintegration
// tests (TestRootDNS...):
//
//   - that the DNS Client picks up a rule from the local key, how long it takes,
//     and whether RefreshPolicyEx is needed (TestRootDNSRegistryWriteWithoutPolicyRefresh
//     reports it);
//   - that the exact-name namespace makes the apex of a domain match;
//   - that "." catches every name and that a rule for a name still wins over it;
//   - that the values above are what the DNS Client accepts (ConfigOptions = 8).
//
// # Rules that outlive the process
//
// The rules are in the registry and survive a crash, a reboot and an
// uninstall. SweepOwnedDNS removes every rule that carries the marker, whoever
// wrote it, without the Reconciler: the uninstall command and the stop of the
// service use it, so that a catch-all rule cannot keep all DNS of the PC on a
// dead server.
package windows
