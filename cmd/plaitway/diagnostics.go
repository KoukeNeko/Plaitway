package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

func (a *app) diagnostics(ctx context.Context, args []string) error {
	fs := a.flagSet("diagnostics")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := a.parse(fs, args, 0, 0); err != nil {
		return err
	}
	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	callCtx, cancel := callContext(ctx)
	defer cancel()
	d, err := c.api.GetDiagnostics(callCtx, &pb.GetDiagnosticsRequest{})
	if err != nil {
		return c.failure(err)
	}
	if *asJSON {
		return a.printJSON(d)
	}
	// Routes and journal entries name their profile by id.
	profiles, err := c.profiles(ctx)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(profiles))
	for _, p := range profiles {
		names[p.Id] = p.Name
	}
	owner := func(id string) string {
		if name, ok := names[id]; ok {
			return name
		}
		return id
	}

	daemon := d.GetDaemon()
	privilege := "privileged"
	if !daemon.GetPrivileged() {
		privilege = "not privileged: it cannot create tunnels or change routes"
	}
	pairs := [][2]string{{"daemon", fmt.Sprintf("%s, protocol %d, %s", daemon.GetVersion(), daemon.GetProtocolVersion(), privilege)}}
	for _, e := range daemon.GetEngines() {
		pairs = append(pairs, [2]string{"engine", engineSummary(e)})
	}
	n := d.GetNetwork()
	pairs = append(pairs,
		[2]string{"default v4", gatewaySummary(n.GetDefaultGatewayV4(), n.GetDefaultInterfaceV4())},
		[2]string{"default v6", gatewaySummary(n.GetDefaultGatewayV6(), n.GetDefaultInterfaceV6())},
		[2]string{"interfaces", dash(strings.Join(n.GetInterfaces(), " "))},
		[2]string{"last change", strings.TrimSpace(formatTime(n.GetLastChange()) + " " + n.GetLastChangeReason())})
	keyValues(a.stdout, pairs)

	if routes := d.GetOwnedRoutes(); len(routes) > 0 {
		rows := make([][]string, len(routes))
		for i, r := range routes {
			rows[i] = []string{r.Prefix, owner(r.Owner), enumName(r.Kind, "ROUTE_KIND_"), dash(r.Via), enumName(r.State, "ROUTE_STATE_")}
		}
		a.section("Owned routes")
		a.table([]string{"PREFIX", "OWNER", "KIND", "VIA", "STATE"}, rows, nil)
	}
	if routes := d.GetStaleRoutes(); len(routes) > 0 {
		rows := make([][]string, len(routes))
		for i, r := range routes {
			rows[i] = []string{r.Key, r.Prefix, dash(r.Gateway), dash(r.Interface), strconv.FormatBool(r.Owned), r.Reason}
		}
		a.section("Stale routes")
		a.table([]string{"KEY", "PREFIX", "GATEWAY", "INTERFACE", "OWNED", "REASON"}, rows, nil)
	}
	if entries := d.GetResolverEntries(); len(entries) > 0 {
		a.section("Resolver entries")
		for _, e := range entries {
			fmt.Fprintln(a.stdout, clean(e))
		}
	}
	if entries := d.GetRecentJournal(); len(entries) > 0 {
		rows := make([][]string, len(entries))
		for i, e := range entries {
			rows[i] = []string{formatTime(e.Time), owner(e.Owner), e.Kind, e.Key, e.State}
		}
		a.section("Journal")
		a.table([]string{"TIME", "OWNER", "KIND", "KEY", "STATE"}, rows, nil)
	}
	return nil
}

func (a *app) section(title string) { fmt.Fprintf(a.stdout, "\n%s\n", title) }

func engineSummary(e *pb.EngineInfo) string {
	if !e.Available {
		return kindName(e.Kind) + ": unavailable, " + e.Detail
	}
	return strings.TrimSpace(kindName(e.Kind) + " " + e.Version)
}

func gatewaySummary(gateway, iface string) string {
	if gateway == "" && iface == "" {
		return "-"
	}
	if gateway == "" {
		return "on " + iface
	}
	return gateway + " on " + dash(iface)
}
