package main

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

func (a *app) set(ctx context.Context, args []string) error {
	fs := a.flagSet("set")
	name := fs.String("name", "", "new name")
	autoConnect := fs.Bool("auto-connect", false, "connect when the daemon starts; -auto-connect=false turns it off")
	tunnelMode := fs.String("tunnel-mode", "", "auto, full or split")
	excludePrivate := fs.Bool("exclude-private-ips", false, "WireGuard: keep the private ranges out of the tunnel; -exclude-private-ips=false turns it off")
	onDemand := fs.String("on-demand", "", "connect on these networks and disconnect on any other: ethernet, wifi, ethernet,wifi, or off")
	rest, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	// The settings travel as one message, so a flag that was not given has to
	// be told from one that was given its zero value.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	changesSettings := slices.ContainsFunc([]string{"auto-connect", "tunnel-mode", "exclude-private-ips", "on-demand"},
		func(flagName string) bool { return given[flagName] })
	if !given["name"] && !changesSettings {
		return usageErrorf("set: nothing to change")
	}
	var mode pb.TunnelMode
	if given["tunnel-mode"] {
		if mode, err = parseTunnelMode(*tunnelMode); err != nil {
			return err
		}
	}
	var rules *pb.OnDemandRules
	if given["on-demand"] {
		if rules, err = parseOnDemand(*onDemand); err != nil {
			return err
		}
	}

	c, err := a.dial()
	if err != nil {
		return err
	}
	defer c.close()
	p, err := c.find(ctx, rest[0])
	if err != nil {
		return err
	}
	req := &pb.UpdateProfileRequest{Id: p.Id}
	if given["name"] {
		req.Name = name
	}
	settings := &pb.ProfileSettings{}
	proto.Merge(settings, p.GetSettings())
	if given["auto-connect"] {
		settings.AutoConnect = *autoConnect
	}
	if given["tunnel-mode"] {
		settings.TunnelMode = mode
	}
	if given["exclude-private-ips"] {
		settings.ExcludePrivateIps = *excludePrivate
	}
	if given["on-demand"] {
		settings.OnDemand = rules
	}
	if changesSettings {
		// Zero keeps the priority the helper has now: the settings above were read a moment ago,
		// and a reorder in the app since would be undone by sending the old one back.
		settings.Priority = 0
		req.Settings = settings
	}

	callCtx, cancel := callContext(ctx)
	defer cancel()
	updated, err := c.api.UpdateProfile(callCtx, req)
	if err != nil {
		return c.failure(err)
	}
	line := "updated " + clean(updated.Name)
	// A profile that on-demand connects because of this very change starts with the new settings.
	if p.DesiredEnabled && updated.DesiredEnabled && appliesOnNextConnection(p.GetSettings(), updated.GetSettings()) {
		line += " (applies on the next connection)"
	}
	fmt.Fprintln(a.stdout, line)
	return nil
}

// appliesOnNextConnection reports whether a change of settings reaches a tunnel
// that is up only when it connects again: the daemon starts an engine with the
// tunnel mode and the private-range option it has then.
func appliesOnNextConnection(before, after *pb.ProfileSettings) bool {
	return tunnelModeName(before.GetTunnelMode()) != tunnelModeName(after.GetTunnelMode()) ||
		before.GetExcludePrivateIps() != after.GetExcludePrivateIps()
}

func parseTunnelMode(value string) (pb.TunnelMode, error) {
	switch strings.ToLower(value) {
	case "auto":
		return pb.TunnelMode_TUNNEL_MODE_AUTO, nil
	case "full":
		return pb.TunnelMode_TUNNEL_MODE_FULL, nil
	case "split":
		return pb.TunnelMode_TUNNEL_MODE_SPLIT, nil
	}
	return 0, usageErrorf("set: -tunnel-mode must be auto, full or split")
}

// tunnelModeName words a mode like the flag does. The daemon treats an
// unspecified mode as auto.
func tunnelModeName(m pb.TunnelMode) string {
	if m == pb.TunnelMode_TUNNEL_MODE_UNSPECIFIED {
		m = pb.TunnelMode_TUNNEL_MODE_AUTO
	}
	return enumName(m, "TUNNEL_MODE_")
}

func parseOnDemand(value string) (*pb.OnDemandRules, error) {
	rules := &pb.OnDemandRules{}
	if strings.EqualFold(strings.TrimSpace(value), "off") {
		return rules, nil
	}
	for network := range strings.SplitSeq(value, ",") {
		switch strings.ToLower(strings.TrimSpace(network)) {
		case "ethernet":
			rules.Ethernet = true
		case "wifi":
			rules.Wifi = true
		default:
			return nil, usageErrorf("set: -on-demand must be ethernet, wifi, ethernet,wifi or off")
		}
	}
	return rules, nil
}

// onDemandName words rules like the flag does.
func onDemandName(r *pb.OnDemandRules) string {
	var networks []string
	if r.GetEthernet() {
		networks = append(networks, "ethernet")
	}
	if r.GetWifi() {
		networks = append(networks, "wifi")
	}
	if len(networks) == 0 {
		return "off"
	}
	return strings.Join(networks, ",")
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// printSettings is the part of a profile that the table of status has no room
// for: its settings and, for WireGuard, the public key that the peer needs.
func (a *app) printSettings(p *pb.Profile) {
	s := p.GetSettings()
	pairs := [][2]string{
		{"auto-connect", onOff(s.GetAutoConnect())},
		{"tunnel mode", tunnelModeName(s.GetTunnelMode())},
	}
	if p.Kind == pb.ProfileKind_PROFILE_KIND_WIREGUARD {
		pairs = append(pairs, [2]string{"exclude private IPs", onOff(s.GetExcludePrivateIps())})
	}
	pairs = append(pairs, [2]string{"on demand", onDemandName(s.GetOnDemand())})
	if key := p.GetSummary().GetPublicKey(); key != "" {
		pairs = append(pairs, [2]string{"public key", key})
	}
	fmt.Fprintln(a.stdout)
	keyValues(a.stdout, pairs)
}
