package ovpn

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

// dnsFrom applies one "dns ..." option per line.
func dnsFrom(t *testing.T, options string) dnsOptions {
	t.Helper()
	var o dnsOptions
	for _, line := range strings.Split(strings.TrimSpace(options), "\n") {
		args, err := splitFields(line)
		if err != nil || len(args) < 2 || args[0] != "dns" {
			t.Fatalf("bad test option %q", line)
		}
		if err := o.add(args[1:]); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
	return o
}

func TestDNSOptionsIntents(t *testing.T) {
	tests := []struct {
		name    string
		options string
		full    bool
		want    []tunnel.DNSIntent
	}{
		{
			name:    "server without domains takes every name in a full tunnel",
			options: "dns server 1 address 10.8.0.1",
			full:    true,
			want:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1"), MatchDomains: []string{"."}}},
		},
		{
			name:    "and the search domains in a split tunnel",
			options: "dns server 1 address 10.8.0.1 10.8.0.2\ndns search-domains corp.example Lab.Example.",
			want:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1", "10.8.0.2"), MatchDomains: []string{"corp.example", "lab.example"}}},
		},
		{
			name:    "a split tunnel without any domain gets no match domain",
			options: "dns server 1 address 10.8.0.1",
			want:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1")}},
		},
		{
			name: "each server answers its own domains, in the order of the numbers",
			options: "dns server 2 address 10.9.0.1\ndns server 2 resolve-domains lab.example\n" +
				"dns server 1 address 10.8.0.1\ndns server 1 resolve-domains corp.example intra.example\n" +
				"dns search-domains ignored.example",
			full: true,
			want: []tunnel.DNSIntent{
				{Servers: addrs("10.8.0.1"), MatchDomains: []string{"corp.example", "intra.example"}},
				{Servers: addrs("10.9.0.1"), MatchDomains: []string{"lab.example"}},
			},
		},
		{
			name:    "IPv6 and a port that is the standard one",
			options: "dns server 1 address 2001:db8::1 [2001:db8::2]:53 10.8.0.1:53",
			want:    []tunnel.DNSIntent{{Servers: addrs("2001:db8::1", "2001:db8::2", "10.8.0.1")}},
		},
		{
			name:    "plain transport and DNSSEC that is not required are fine",
			options: "dns server 1 address 10.8.0.1\ndns server 1 transport plain\ndns server 1 dnssec optional\ndns server 1 dnssec no",
			want:    []tunnel.DNSIntent{{Servers: addrs("10.8.0.1")}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dnsFrom(t, tt.options).intents(tt.full); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("intents = %+v\nwant     %+v", got, tt.want)
			}
		})
	}
}

// A server whose settings a resolver entry cannot express is skipped, as
// openvpn skips it for its own DNS helper, and the option says why.
func TestDNSOptionsSkipServersThatCannotBeConfigured(t *testing.T) {
	for _, opts := range []struct{ name, options, wantErr string }{
		{"DNS over TLS", "dns server 1 address 10.8.0.1\ndns server 1 transport DoT", "transport DoT"},
		{"DNS over HTTPS", "dns server 1 transport DoH\ndns server 1 address 10.8.0.1", "transport DoH"},
		{"required DNSSEC", "dns server 1 address 10.8.0.1\ndns server 1 dnssec yes", "DNSSEC"},
		{"a port that is not 53", "dns server 1 address 10.8.0.1:853", "port 853"},
	} {
		t.Run(opts.name, func(t *testing.T) {
			var (
				o   dnsOptions
				got string
			)
			for _, line := range strings.Split(opts.options, "\n") {
				args, _ := splitFields(line)
				if err := o.add(args[1:]); err != nil {
					got = err.Error()
				}
			}
			if !strings.Contains(got, opts.wantErr) {
				t.Errorf("error = %q, want it to mention %q", got, opts.wantErr)
			}
			if i := o.intents(true); len(i) != 0 {
				t.Errorf("the server was used anyway: %+v", i)
			}
		})
	}
}

func TestDNSOptionsRejectBadInput(t *testing.T) {
	for _, args := range []string{
		"", "search-domains", "server 1", "server 1 address", "server x address 10.8.0.1", "server 200 address 10.8.0.1",
		"server 1 address not-an-address", "server 1 address 0.0.0.0", "server 1 address 127.0.0.1", "server 1 address 224.0.0.1",
		"server 1 resolve-domains 10.1.1.1", "server 1 resolve-domains bad_domain!", "search-domains 1.2.3.4",
		"unknown value", "server 1 unknown value",
	} {
		fields := strings.Fields(args)
		var o dnsOptions
		if err := o.add(fields); err == nil {
			t.Errorf("add(%q) succeeded", args)
		}
		if i := o.intents(true); len(i) != 0 {
			t.Errorf("add(%q) left a usable server: %+v", args, i)
		}
	}
}

func TestDNSOptionsBoundTheDomainLists(t *testing.T) {
	var o dnsOptions
	for i := 0; i < maxDNSDomains; i++ {
		d := fmt.Sprintf("d%d.example", i)
		if err := o.add([]string{"search-domains", d}); err != nil {
			t.Fatal(err)
		}
		if err := o.add([]string{"server", "1", "resolve-domains", d}); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.add([]string{"search-domains", "one-more.example"}); err == nil {
		t.Error("a search domain over the limit was accepted")
	}
	if err := o.add([]string{"server", "1", "resolve-domains", "one-more.example"}); err == nil {
		t.Error("a resolve domain over the limit was accepted")
	}
}

func TestDNSOptionsWithPulled(t *testing.T) {
	profile := dnsFrom(t, "dns server 1 address 10.1.0.1\ndns server 1 resolve-domains static.example\ndns server 5 address 10.5.0.1\ndns search-domains one.example")
	pulled := dnsFrom(t, "dns server 1 address 10.8.0.1\ndns server 2 address 10.8.0.2\ndns search-domains two.example one.example")

	merged := profile.withPulled(pulled)
	got := merged.intents(false)
	want := []tunnel.DNSIntent{
		// A pushed server replaces the profile's server of the same number,
		// with its domains.
		{Servers: addrs("10.8.0.1"), MatchDomains: []string{"one.example", "two.example"}},
		{Servers: addrs("10.8.0.2"), MatchDomains: []string{"one.example", "two.example"}},
		{Servers: addrs("10.5.0.1"), MatchDomains: []string{"one.example", "two.example"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged intents = %+v\nwant           %+v", got, want)
	}
	// Neither input changed.
	if i := profile.intents(false); len(i) != 2 || !reflect.DeepEqual(i[0].Servers, addrs("10.1.0.1")) {
		t.Errorf("the profile's own options changed: %+v", i)
	}
	if got := (dnsOptions{}).withPulled(pulled).intents(true); len(got) != 2 {
		t.Errorf("options pushed to a profile without any: %+v", got)
	}
	if got := profile.withPulled(dnsOptions{}).intents(true); len(got) != 2 {
		t.Errorf("a push without dns options changed the profile's: %+v", got)
	}
}

func TestParsePushReply(t *testing.T) {
	tests := []struct {
		line string
		want pushReply
		ok   bool
	}{
		{
			"PUSH: Received control message: 'PUSH_REPLY,route 10.20.0.0 255.255.0.0,dns server 1 address 10.8.0.1,dns search-domains corp.example,ifconfig 10.8.0.2 255.255.255.0,peer-id 0'",
			pushReply{options: []string{"route 10.20.0.0 255.255.0.0", "dns server 1 address 10.8.0.1", "dns search-domains corp.example", "ifconfig 10.8.0.2 255.255.255.0", "peer-id 0"}},
			true,
		},
		{
			"PUSH: Received control message: 'PUSH_REPLY,dns server 1 address 10.8.0.1,push-continuation 2'",
			pushReply{options: []string{"dns server 1 address 10.8.0.1", "push-continuation 2"}, more: true},
			true,
		},
		{"PUSH: Received control message: 'PUSH_REPLY,push-continuation 1'", pushReply{options: []string{"push-continuation 1"}}, true},
		{"PUSH: Received control message: 'PUSH_REQUEST'", pushReply{}, false},
		{"Initialization Sequence Completed", pushReply{}, false},
	}
	for _, tt := range tests {
		got, ok := parsePushReply(tt.line)
		if ok != tt.ok || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parsePushReply(%q) = %+v, %v; want %+v, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

// What an engine does with the replies: a reply that continues in the next
// message is collected as one, a new reply replaces the old one, and the
// profile's pull-filters apply to what is pushed.
func TestEngineCollectsPushedDNSOptions(t *testing.T) {
	e := newTestEngine(t, shortTempDir(t), minimalProfile+"pull-filter ignore \"dns server 2\"\npull-filter accept \"dns\"\n")
	logLine := func(options string) {
		t.Helper()
		if err := e.handle(logEvent{Msg: pushReplyPrefix + options + "'"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	servers := func() []tunnel.DNSIntent { return e.s.pushedDNS.intents(true) }

	logLine("dns server 1 address 10.8.0.1,dns server 2 address 10.8.0.2,push-continuation 2")
	logLine("dns server 3 address 10.8.0.3,ifconfig 10.8.0.2 255.255.255.0,push-continuation 1")
	if want := []tunnel.DNSIntent{
		{Servers: addrs("10.8.0.1"), MatchDomains: []string{"."}},
		{Servers: addrs("10.8.0.3"), MatchDomains: []string{"."}},
	}; !reflect.DeepEqual(servers(), want) {
		t.Errorf("pushed = %+v\nwant     %+v (server 2 is pull-filtered)", servers(), want)
	}

	// The next connection pushes its own.
	logLine("dns server 1 address 10.9.0.1,dhcp-option DNS 10.9.0.77")
	if want := []tunnel.DNSIntent{{Servers: addrs("10.9.0.1"), MatchDomains: []string{"."}}}; !reflect.DeepEqual(servers(), want) {
		t.Errorf("pushed = %+v, want %+v", servers(), want)
	}

	// A bad option is reported and the rest is applied.
	var logs []string
	e.deps.Log = func(_ tunnel.LogLevel, text string) { logs = append(logs, text) }
	logLine("dns server 1 address bogus,dns server 1 address 10.9.0.5")
	if !strings.Contains(strings.Join(logs, "\n"), `pushed option "dns server 1 address bogus" not applied`) {
		t.Errorf("logs = %q", logs)
	}
	if want := []tunnel.DNSIntent{{Servers: addrs("10.9.0.5"), MatchDomains: []string{"."}}}; !reflect.DeepEqual(servers(), want) {
		t.Errorf("pushed = %+v, want %+v", servers(), want)
	}
}
