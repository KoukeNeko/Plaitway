using System.Globalization;
using Plaitway.AppCore.Logs;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/App/ModelTests.swift: PresentationTests, StatusPresentationTests, DiagnosticsReportTests.</summary>
public sealed class PresentationTests
{
    private static readonly CultureInfo English = CultureInfo.GetCultureInfo("en-US");

    [Fact]
    public void MovesProfilesLikeAListDrag()
    {
        string[] ids = ["a", "b", "c", "d"];

        Assert.Equal(["d", "a", "b", "c"], ProfileOrder.Moving(ids, [3], 0));
        Assert.Equal(["b", "c", "d", "a"], ProfileOrder.Moving(ids, [0], 4));
        Assert.Equal(["a", "c", "b", "d"], ProfileOrder.Moving(ids, [1], 3));
        Assert.Equal(["c", "d", "a", "b"], ProfileOrder.Moving(ids, [0, 1], 4));
    }

    [Fact]
    public void MovesOneProfileByOnePlace()
    {
        string[] ids = ["a", "b", "c"];

        Assert.Equal(["b", "a", "c"], ProfileOrder.Moving(ids, "b", -1));
        Assert.Equal(["a", "c", "b"], ProfileOrder.Moving(ids, "b", 1));
        Assert.Null(ProfileOrder.Moving(ids, "a", -1));
        Assert.Null(ProfileOrder.Moving(ids, "c", 1));
        Assert.Null(ProfileOrder.Moving(ids, "x", 1));
    }

    [Fact]
    public void RouteRowsShowStatesAndNameTheProfileThatShadows()
    {
        var text = TestText.En;
        var profile = Profiles.Make(name: "Lab", state: ProfileState.Connected);
        profile.Status = new TunnelStatus();
        profile.Status.Routes.Add(new RouteStatus { Prefix = "192.168.1.0/24", State = RouteState.Installed });
        profile.Status.Routes.Add(new RouteStatus { Prefix = "10.99.0.0/16", State = RouteState.Shadowed, ShadowedBy = "other", Detail = "held by a profile with higher priority" });
        profile.Status.Routes.Add(new RouteStatus { Prefix = "192.168.0.0/24", State = RouteState.Blocked });

        var rows = RouteRow.For(profile, text, id => id == "other" ? "Office" : null);

        Assert.Equal(["192.168.1.0/24", "10.99.0.0/16", "192.168.0.0/24"], rows.Select(row => row.Prefix));
        Assert.Equal([RouteState.Installed, RouteState.Shadowed, RouteState.Blocked], rows.Select(row => row.State));
        Assert.Equal(RouteState.Installed.Label(text), rows[0].StateLabel);
        Assert.Equal(RouteState.Shadowed.Label(text, "Office"), rows[1].StateLabel);
        Assert.Equal("held by a profile with higher priority", rows[1].Detail);
        Assert.Equal(RouteState.Blocked.Label(text), rows[2].StateLabel);

        // A profile that is gone is still named, by its id.
        Assert.Equal(RouteState.Shadowed.Label(text, "other"), RouteRow.For(profile, text, _ => null)[1].StateLabel);
    }

    [Fact]
    public void ADisconnectedProfileShowsTheRoutesItNames()
    {
        var profile = Profiles.Make();
        profile.Summary = new ProfileSummary();
        profile.Summary.Routes.Add("192.168.1.0/24");

        var rows = RouteRow.For(profile, TestText.En, _ => null);

        Assert.Equal(["192.168.1.0/24"], rows.Select(row => row.Prefix));
        Assert.Null(rows[0].State);
        Assert.Null(rows[0].StateLabel);
    }

    [Fact]
    public void DnsRowsTurnTheDotIntoEveryDomain()
    {
        var profile = Profiles.Make(state: ProfileState.Connected);
        profile.Status = new TunnelStatus();
        var everything = new DnsStatus { State = RouteState.Installed };
        everything.Servers.AddRange(["10.8.0.1", "10.8.0.2"]);
        everything.MatchDomains.Add(".");
        var corporate = new DnsStatus { State = RouteState.Failed, Detail = "refused" };
        corporate.Servers.Add("10.9.0.1");
        corporate.MatchDomains.AddRange(["corp.example", "lan"]);
        profile.Status.Dns.AddRange([everything, corporate]);

        var rows = DnsRow.For(profile, TestText.En);

        Assert.Equal(["10.8.0.1, 10.8.0.2", "10.9.0.1"], rows.Select(row => row.Servers));
        Assert.Equal(TestText.En.AllDomains, rows[0].Domains);
        Assert.Equal("corp.example, lan", rows[1].Domains);
        Assert.Equal(RouteState.Failed, rows[1].State);
        Assert.Equal("refused", rows[1].Detail);
    }

    [Fact]
    public void EveryStateHasALabel()
    {
        foreach (var state in new[]
        {
            ProfileState.Connected, ProfileState.Connecting, ProfileState.Disconnecting, ProfileState.Disconnected, ProfileState.Failed,
            ProfileState.Reconnecting, ProfileState.AwaitingCredentials, ProfileState.Unspecified,
        })
        {
            Assert.False(string.IsNullOrEmpty(state.Label(TestText.En)), state.ToString());
            Assert.False(string.IsNullOrEmpty(state.Label(TestText.Zh)), state.ToString());
        }

        // Product names are not translated.
        Assert.Equal("OpenVPN", ProfileKind.Openvpn.Label(TestText.Zh));
        Assert.Equal("WireGuard", ProfileKind.Wireguard.Label(TestText.Zh));
        Assert.Equal([TunnelMode.Auto, TunnelMode.Full, TunnelMode.Split], Labels.TunnelModeChoices);
    }

    [Fact]
    public void FormatsByteCountsAndEndpoints()
    {
        Assert.False(string.IsNullOrEmpty(Formatting.Bytes(0, English)));

        // Decimal units, as Explorer counts them.
        var megabytes = Formatting.Bytes(1_500_000, English);
        Assert.Contains("1.5", megabytes, StringComparison.Ordinal);
        Assert.Contains("MB", megabytes, StringComparison.Ordinal);

        // A counter at the top of its range does not overflow.
        Assert.False(string.IsNullOrEmpty(Formatting.Bytes(ulong.MaxValue, English)));
        Assert.Equal("vpn.example.net:1194 (tcp)", Formatting.Endpoint("vpn.example.net", 1194, "tcp"));
        Assert.Equal("vpn.example.net", Formatting.Endpoint("vpn.example.net", 0, string.Empty));
    }

    [Fact]
    public void LogLinesAreCopiedAsPlainText()
    {
        var line = new LogEntry(1, null, LogLevel.Warn, "something odd");
        Assert.Equal("WARN something odd", line.PlainText);

        var timed = new LogEntry(2, DateTimeOffset.FromUnixTimeSeconds(0), LogLevel.Info, "up");

        // A time of day of the same width in every language: HH:mm:ss.
        Assert.Equal("00:00:00 INFO up".Length, timed.PlainText.Length);
        Assert.EndsWith(" INFO up", timed.PlainText, StringComparison.Ordinal);
        Assert.Equal(8, Formatting.LogTime(DateTimeOffset.FromUnixTimeSeconds(0)).Length);
    }

    [Fact]
    public void FormatsARateAndLeavesTheLogTimeTheSameWidth()
    {
        Assert.Contains("MB", Formatting.Rate(1_500_000, TestText.En), StringComparison.Ordinal);
        Assert.Contains('/', Formatting.Rate(0, TestText.En));
        Assert.Contains('/', Formatting.Rate(-5, TestText.En));
        Assert.All([0L, (3600L * 13) + 61, 86_399L], seconds => Assert.Equal(8, Formatting.LogTime(DateTimeOffset.FromUnixTimeSeconds(seconds)).Length));
    }

    // StatusPresentationTests

    private static readonly ProfileState[] ProfileStates =
    [
        ProfileState.Disconnected, ProfileState.Connecting, ProfileState.Connected, ProfileState.Disconnecting, ProfileState.Failed,
        ProfileState.Reconnecting, ProfileState.AwaitingCredentials,
    ];

    [Fact]
    public void NoTwoProfileStatesShareAShape()
    {
        // Colour is the third channel: the glyph and the word already tell the states apart.
        var icons = ProfileStates.Select(StatusVisuals.Icon).ToList();

        Assert.Equal(icons.Count, icons.Distinct().Count());
    }

    [Fact]
    public void TheSidebarUsesTheShieldsOfTheTrayIcon()
    {
        // A profile is the shield the tray icon draws for the same state.
        Assert.Equal(ProfileState.Connected.Icon(), AggregateState.Connected.Icon());
        Assert.Equal(ProfileState.Disconnected.Icon(), AggregateState.Idle.Icon());
        Assert.Equal(ProfileState.Connecting.Icon(), AggregateState.Connecting.Icon());
        Assert.Equal(ProfileState.AwaitingCredentials.Icon(), AggregateState.NeedsCredentials.Icon());
    }

    [Fact]
    public void AStandbyRouteIsNotPaintedAsAWarning()
    {
        Assert.Equal(StatusTone.Neutral, RouteState.Shadowed.Tone());
        Assert.NotEqual(RouteState.Shadowed.Tone(), RouteState.Blocked.Tone());
        Assert.True(RouteState.Blocked.IsLost() && RouteState.Failed.IsLost());
        Assert.False(RouteState.Shadowed.IsLost() || RouteState.Installed.IsLost() || RouteState.Pending.IsLost());
    }

    [Fact]
    public void EveryRouteStateHasItsOwnShape()
    {
        var states = new[] { RouteState.Pending, RouteState.Installed, RouteState.Shadowed, RouteState.Blocked, RouteState.Failed };

        Assert.Equal(states.Length, states.Select(StatusVisuals.Icon).Distinct().Count());
    }

    [Fact]
    public void OnlyAnEndingStateMoves()
    {
        Assert.Equal(
            [ProfileState.Connecting, ProfileState.Disconnecting, ProfileState.Reconnecting],
            ProfileStates.Where(StatusVisuals.IsTransitional));
    }

    [Fact]
    public void EveryProfileSectionHasANameInBothLanguages()
    {
        Assert.Equal(5, SectionLabels.ProfileSections.Count);
        foreach (var text in new[] { TestText.En, TestText.Zh })
        {
            var labels = SectionLabels.ProfileSections.Select(section => section.Label(text)).ToList();
            Assert.All(labels, label => Assert.False(string.IsNullOrEmpty(label)));
            Assert.Equal(labels.Count, labels.Distinct().Count());
        }
    }

    // DiagnosticsReportTests

    [Fact]
    public void ReportListsWhatABugReportNeedsAndNoLogLines()
    {
        var diagnostics = new Diagnostics
        {
            Network = new NetworkInfo { DefaultGatewayV4 = "192.168.1.1", DefaultInterfaceV4 = "Ethernet" },
        };
        diagnostics.Network.Interfaces.AddRange(["Loopback", "Ethernet", "Plaitway-p1"]);
        diagnostics.OwnedRoutes.Add(new OwnedRoute { Prefix = "10.20.0.0/16", Owner = "p1", Via = "Plaitway-p1", State = RouteState.Installed, Kind = RouteKind.Tunnel });
        diagnostics.StaleRoutes.Add(new StaleRoute
        {
            Prefix = "203.0.113.9/32",
            Gateway = "192.168.0.254",
            Interface = "Ethernet",
            Reason = "the gateway is not on the interface's subnet",
            Owned = true,
        });
        diagnostics.ResolverEntries.Add("Plaitway-p1 corp.example 10.20.0.1");

        var report = DiagnosticsReport.Text(diagnostics, "0.2.0", daemon: null, TestText.En, id => id == "p1" ? "Office" : null);

        Assert.StartsWith("Plaitway 0.2.0", report, StringComparison.Ordinal);
        Assert.Contains("gateway IPv4: 192.168.1.1 Ethernet", report, StringComparison.Ordinal);
        Assert.Contains("10.20.0.0/16", report, StringComparison.Ordinal);
        Assert.Contains("(Office)", report, StringComparison.Ordinal);
        Assert.Contains("203.0.113.9/32 via 192.168.0.254 Ethernet: the gateway is not on the interface's subnet (installed by Plaitway)", report, StringComparison.Ordinal);
        Assert.Contains("Plaitway-p1 corp.example 10.20.0.1", report, StringComparison.Ordinal);
    }
}
