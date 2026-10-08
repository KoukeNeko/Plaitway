using Grpc.Core;
using Plaitway.Client.Config;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Client;

/// <summary>macos/Tests/PlaitwayTests/Client/ProfileManagementTests.swift against the real daemon.</summary>
public sealed class ProfileManagementTests(DaemonBinary binary)
{
    // import

    [Fact]
    public async Task ImportsAnOpenVpnProfile()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var result = await session.Client.ImportProfileAsync(Fixture.OpenVpn("vpn.example.net"), @"C:\Users\someone\Router.ovpn");

        Assert.Equal(ProfileKind.Openvpn, result.Profile.Kind);
        Assert.Equal("Router", result.Profile.Name);
        Assert.Equal(ProfileState.Disconnected, result.Profile.State);
        Assert.Equal("vpn.example.net", result.Profile.Summary.Endpoints[0].Host);
        Assert.Equal("tcp", result.Profile.Summary.Endpoints[0].Protocol);
        Assert.Equal(["192.168.1.0/24"], result.Profile.Summary.Routes);
        Assert.Empty(result.Warnings);
        await DaemonSession.WaitUntilAsync("the profile to appear", () => session.Profile(result.Profile.Id) is not null);
    }

    [Fact]
    public async Task ImportsAWireGuardProfileAndKnowsItIsAFullTunnel()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var result = await session.Client.ImportProfileAsync(Fixture.WireGuard(), "home.conf", "Home router");

        Assert.Equal(ProfileKind.Wireguard, result.Profile.Kind);
        Assert.Equal("Home router", result.Profile.Name);
        Assert.True(result.Profile.Summary.RedirectsDefaultRoute);
        Assert.Equal(["10.6.0.2/32"], result.Profile.Summary.Addresses);
    }

    [Fact]
    public async Task ReturnsWhatTheDaemonRemoved()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var result = await session.Client.ImportProfileAsync(Fixture.OpenVpn(extra: ["up /bin/sh"]), "scripted.ovpn");

        Assert.Equal(["up"], result.Warnings.Select(warning => warning.Directive));
        Assert.Equal(6, result.Warnings[0].Line);
        Assert.NotEmpty(result.Warnings[0].Message);
    }

    [Fact]
    public async Task ShowsTheReasonARejectedProfileGives()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var error = await Assert.ThrowsAsync<RpcException>(
            () => session.Client.ImportProfileAsync(Fixture.OpenVpn(markers: ["# fake: reject"]), "bad.ovpn"));

        Assert.Equal(StatusCode.InvalidArgument, error.StatusCode);
        Assert.Contains("rejected", error.Status.Detail, StringComparison.Ordinal);
        Assert.Equal(new DaemonFailure(DaemonFailureKind.Rejected, error.Status.Detail), DaemonFailure.From(error));
        Assert.Empty(session.Profiles);
    }

    [Fact]
    public async Task RefusesEmptyContent()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        await Assert.ThrowsAsync<RpcException>(() => session.Client.ImportProfileAsync(Array.Empty<byte>(), "empty.ovpn"));
    }

    // update

    [Fact]
    public async Task UpdatesNameAndSettings()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        var settings = session.Profile(id)!.Settings.Clone();
        var firstPriority = settings.Priority;
        settings.AutoConnect = true;
        settings.TunnelMode = TunnelMode.Split;

        await session.Client.UpdateProfileAsync(id, "Office", settings);

        await DaemonSession.WaitUntilAsync("the update", () => session.Profile(id)?.Name == "Office");
        Assert.True(session.Profile(id)!.Settings.AutoConnect);
        Assert.Equal(TunnelMode.Split, session.Profile(id)!.Settings.TunnelMode);
        Assert.Equal(firstPriority, session.Profile(id)!.Settings.Priority);
    }

    [Fact]
    public async Task UpdatingOnlyTheNameKeepsTheSettings()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync(
            "office.ovpn", settings: new ProfileSettings { AutoConnect = true, TunnelMode = TunnelMode.Full });

        await session.Client.UpdateProfileAsync(id, "Renamed");

        await DaemonSession.WaitUntilAsync("the rename", () => session.Profile(id)?.Name == "Renamed");
        Assert.True(session.Profile(id)!.Settings.AutoConnect);
        Assert.Equal(TunnelMode.Full, session.Profile(id)!.Settings.TunnelMode);
    }

    [Fact]
    public async Task UpdatingAnUnknownProfileIsNotFound()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileAsync("nope", "x"));
        Assert.Equal(StatusCode.NotFound, error.StatusCode);
    }

    // reorder

    [Fact]
    public async Task ReordersByPriority()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var a = await session.ImportFixtureAsync("a.ovpn");
        var b = await session.ImportFixtureAsync("b.ovpn");
        var c = await session.ImportFixtureAsync("c.conf", Fixture.WireGuard());
        await DaemonSession.WaitUntilAsync("three profiles", () => session.Profiles.Count == 3);
        Assert.Equal([a, b, c], session.Profiles.Select(profile => profile.Id));

        var reordered = await session.Client.ReorderProfilesAsync([c, a, b]);

        Assert.Equal([c, a, b], reordered.Select(profile => profile.Id));
        // The daemon reports the new priorities one profile at a time; the list settles on the last.
        await DaemonSession.WaitUntilAsync(
            "the new order",
            () => session.Profiles.Select(profile => profile.Id).SequenceEqual([c, a, b])
                && session.Profiles.Select(profile => profile.Settings.Priority).SequenceEqual([1, 2, 3]));
        await session.Client.ReorderProfilesAsync([b, c, a]);
        await DaemonSession.WaitUntilAsync("the second order", () => session.Profiles.Select(profile => profile.Id).SequenceEqual([b, c, a]));
    }

    [Fact]
    public async Task AReorderThatNamesAProfileTwiceOrLeavesOneOutIsRefused()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var a = await session.ImportFixtureAsync("a.ovpn");
        var b = await session.ImportFixtureAsync("b.ovpn");
        await DaemonSession.WaitUntilAsync("two profiles", () => session.Profiles.Count == 2);

        await Assert.ThrowsAsync<RpcException>(() => session.Client.ReorderProfilesAsync([a]));
        await Assert.ThrowsAsync<RpcException>(() => session.Client.ReorderProfilesAsync([a, a]));

        Assert.Equal([a, b], session.Profiles.Select(profile => profile.Id));
    }

    // delete

    [Fact]
    public async Task DeletesAProfileAndDisconnectsItFirst()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var keep = await session.ImportFixtureAsync("keep.ovpn");
        var doomed = await session.ImportFixtureAsync("doomed.ovpn");
        await session.Client.SetProfileEnabledAsync(doomed, true);
        await session.WaitForStateAsync(doomed, ProfileState.Connected);

        await session.Client.DeleteProfileAsync(doomed);

        await DaemonSession.WaitUntilAsync("the profile to go", () => session.Profile(doomed) is null);
        Assert.Equal([keep], session.Profiles.Select(profile => profile.Id));
    }

    [Fact]
    public async Task DeletingAnUnknownProfileIsNotFound()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.DeleteProfileAsync("nope"));
        Assert.Equal(StatusCode.NotFound, error.StatusCode);
    }

    [Fact]
    public async Task ProfilesSurviveARestartOfTheDaemonInTheirOrder()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var a = await session.ImportFixtureAsync("a.ovpn");
        var b = await session.ImportFixtureAsync("b.ovpn");
        await session.Client.ReorderProfilesAsync([b, a]);
        await DaemonSession.WaitUntilAsync("the order", () => session.Profiles.Select(profile => profile.Id).SequenceEqual([b, a]));
        var snapshots = session.Snapshots;

        session.Daemon.Kill();
        await DaemonSession.WaitUntilAsync("the outage", () => !session.IsAvailable);
        await session.Daemon.StartAsync();
        await DaemonSession.WaitUntilAsync("the reconnect", () => session.IsAvailable && session.Snapshots > snapshots);

        Assert.Equal([b, a], session.Profiles.Select(profile => profile.Id));
    }

    // settings

    [Fact]
    public async Task ExcludingPrivateIpsIsStoredForAWireGuardProfile()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("home.conf", Fixture.WireGuard());
        Assert.False(session.Profile(id)!.Settings.ExcludePrivateIps);
        var settings = session.Profile(id)!.Settings.Clone();
        settings.ExcludePrivateIps = true;

        await session.Client.UpdateProfileAsync(id, settings: settings);

        await DaemonSession.WaitUntilAsync("the setting", () => session.Profile(id)?.Settings.ExcludePrivateIps == true);
        Assert.Equal(settings.Priority, session.Profile(id)!.Settings.Priority);
    }

    [Fact]
    public async Task ExcludingPrivateIpsIsRefusedForAnOpenVpnProfile()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        var settings = session.Profile(id)!.Settings.Clone();
        settings.ExcludePrivateIps = true;

        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileAsync(id, settings: settings));

        Assert.Equal(StatusCode.InvalidArgument, error.StatusCode);
        Assert.Equal("excluding private IPs works on WireGuard's AllowedIPs, and this profile is OpenVPN", error.Status.Detail);
        Assert.False(session.Profile(id)!.Settings.ExcludePrivateIps);
    }

    [Fact]
    public async Task OnDemandRulesAreStoredAndFollowTheNetwork()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        Assert.False(session.Profile(id)!.Settings.OnDemand.IsActive());

        // The fake daemon's network is Wi-Fi. Saving the rules applies them to it at once.
        var settings = session.Profile(id)!.Settings.Clone();
        settings.OnDemand = new OnDemandRules { Wifi = true };
        await session.Client.UpdateProfileAsync(id, settings: settings);
        await session.WaitForStateAsync(id, ProfileState.Connected);
        Assert.True(session.Profile(id)!.Settings.OnDemand.Wifi);
        Assert.False(session.Profile(id)!.Settings.OnDemand.Ethernet);
        Assert.True(session.Profile(id)!.Settings.OnDemand.IsActive());

        settings = session.Profile(id)!.Settings.Clone();
        settings.OnDemand = new OnDemandRules { Ethernet = true };
        await session.Client.UpdateProfileAsync(id, settings: settings);
        await session.WaitForStateAsync(id, ProfileState.Disconnected);
        Assert.True(session.Profile(id)!.Settings.OnDemand.Ethernet);
    }

    [Fact]
    public async Task ClearingBothRulesTurnsOnDemandOff()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var settings = new ProfileSettings { OnDemand = new OnDemandRules { Ethernet = true } };
        var id = await session.ImportFixtureAsync("office.ovpn", settings: settings);
        Assert.True(session.Profile(id)!.Settings.OnDemand.IsActive());

        settings.OnDemand = new OnDemandRules();
        await session.Client.UpdateProfileAsync(id, settings: settings);

        await DaemonSession.WaitUntilAsync("on demand to be off", () => session.Profile(id)?.Settings.OnDemand.IsActive() == false);
    }

    [Theory]
    [InlineData(false, false, false)]
    [InlineData(true, false, true)]
    [InlineData(false, true, true)]
    [InlineData(true, true, true)]
    public void OnDemandIsActiveWhenEitherNetworkIsChecked(bool ethernet, bool wifi, bool active) =>
        Assert.Equal(active, new OnDemandRules { Ethernet = ethernet, Wifi = wifi }.IsActive());
}

/// <summary>macos/Tests/PlaitwayTests/Client/ProfileContentTests.swift against the real daemon (the fake backend's parser).</summary>
public sealed class ProfileContentTests(DaemonBinary binary)
{
    // read

    [Fact]
    public async Task ReadsTheStoredTextNotWhatWasUploaded()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn", Fixture.OpenVpn(extra: ["up /bin/sh"]));

        Assert.Equal(Fixture.OpenVpnText(), await session.Client.GetProfileContentAsync(id));
    }

    [Fact]
    public async Task KeepsAByteOrderMarkTheTextStartsWith()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        byte[] bom = [0xEF, 0xBB, 0xBF];
        var id = await session.ImportFixtureAsync("bom.ovpn", [.. bom, .. Fixture.OpenVpn()]);

        Assert.StartsWith("\uFEFFclient", await session.Client.GetProfileContentAsync(id), StringComparison.Ordinal);
    }

    [Fact]
    public async Task ReadingAnUnknownProfileIsNotFound()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.GetProfileContentAsync("nope"));
        Assert.Equal(StatusCode.NotFound, error.StatusCode);
    }

    // update

    [Fact]
    public async Task ReplacesTheTextAndRefreshesTheSummary()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("home.conf", Fixture.WireGuard());
        var before = session.Profile(id)!.Summary;
        Assert.NotEmpty(before.PublicKey);

        var text = Fixture.WireGuardText("10.9.0.0/16", "198.51.100.4:51821")
            .Replace("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=", StringComparison.Ordinal);
        var result = await session.Client.UpdateProfileContentAsync(id, text + "PostUp = /bin/evil\n");

        Assert.Equal(text, await session.Client.GetProfileContentAsync(id));
        Assert.Equal(id, result.Profile.Id);
        Assert.Equal("198.51.100.4", result.Profile.Summary.Endpoints[0].Host);
        Assert.Equal(51821u, result.Profile.Summary.Endpoints[0].Port);
        Assert.Equal(["10.9.0.0/16"], result.Profile.Summary.Routes);
        Assert.NotEmpty(result.Profile.Summary.PublicKey);
        Assert.NotEqual(before.PublicKey, result.Profile.Summary.PublicKey);
        Assert.Equal(["PostUp"], result.Warnings.Select(warning => warning.Directive));
        Assert.Equal(text.Count(character => character == '\n') + 1, result.Warnings[0].Line);
        await DaemonSession.WaitUntilAsync(
            "the watch to show the new summary", () => session.Profile(id)?.Summary.PublicKey == result.Profile.Summary.PublicKey);
    }

    [Fact]
    public async Task UpdatingTheTextKeepsTheNameAndTheSettings()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var settings = new ProfileSettings { AutoConnect = true, TunnelMode = TunnelMode.Split };
        var id = (await session.Client.ImportProfileAsync(Fixture.OpenVpn(), "office.ovpn", "Office", settings)).Profile.Id;

        var result = await session.Client.UpdateProfileContentAsync(id, Fixture.OpenVpnText("edited.example.net"));

        Assert.Equal("Office", result.Profile.Name);
        Assert.True(result.Profile.Settings.AutoConnect);
        Assert.Equal(TunnelMode.Split, result.Profile.Settings.TunnelMode);
        Assert.Equal("edited.example.net", result.Profile.Summary.Endpoints[0].Host);
    }

    [Fact]
    public async Task TextOfTheOtherKindIsRefusedWithTheDaemonsMessage()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var office = await session.ImportFixtureAsync("office.ovpn");
        var home = await session.ImportFixtureAsync("home.conf", Fixture.WireGuard());

        foreach (var (id, content, message) in new[]
        {
            (office, Fixture.WireGuardText(), "this profile is OpenVPN, but the text is WireGuard"),
            (home, Fixture.OpenVpnText(), "this profile is WireGuard, but the text is OpenVPN"),
        })
        {
            var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileContentAsync(id, content));
            Assert.Equal(StatusCode.InvalidArgument, error.StatusCode);
            Assert.Equal(message, error.Status.Detail);
            Assert.Equal(new DaemonFailure(DaemonFailureKind.Rejected, message), DaemonFailure.From(error));
            Assert.Equal(new ConfigDiagnostic(null, message), ConfigDiagnostic.FromError(error));
        }

        Assert.Equal(Fixture.OpenVpnText(), await session.Client.GetProfileContentAsync(office));
        Assert.Equal(Fixture.WireGuardText(), await session.Client.GetProfileContentAsync(home));
    }

    [Fact]
    public async Task RejectedTextChangesNothing()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");

        var error = await Assert.ThrowsAsync<RpcException>(
            () => session.Client.UpdateProfileContentAsync(id, Fixture.OpenVpnText(markers: ["# fake: reject"])));

        Assert.Equal(StatusCode.InvalidArgument, error.StatusCode);
        Assert.StartsWith("line 6: ", error.Status.Detail, StringComparison.Ordinal);
        Assert.Equal(6, ConfigDiagnostic.FromError(error)?.Line);
        Assert.Equal(Fixture.OpenVpnText(), await session.Client.GetProfileContentAsync(id));
        Assert.Equal("vpn.example.net", session.Profile(id)!.Summary.Endpoints[0].Host);
    }

    [Fact]
    public async Task EmptyTextIsRefused()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileContentAsync(id, string.Empty));
    }

    [Fact]
    public async Task UpdatingAnUnknownProfileIsNotFound()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileContentAsync("nope", Fixture.OpenVpnText()));
        Assert.Equal(StatusCode.NotFound, error.StatusCode);
    }

    // reconnect

    [Fact]
    public async Task TheNewTextOfARunningProfileWaitsForTheNextConnection()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.Connected);
        var tunnel = session.Profile(id)!.Status.InterfaceName;

        // A text that makes the fake engine fail to connect, so a restart is visible.
        await session.Client.UpdateProfileContentAsync(id, Fixture.OpenVpnText(markers: ["# fake: fail"]), reconnect: false);
        await Task.Delay(TimeSpan.FromMilliseconds(300));
        Assert.Equal(ProfileState.Connected, session.Profile(id)?.State);
        Assert.Equal(tunnel, session.Profile(id)!.Status.InterfaceName);

        await session.Client.SetProfileEnabledAsync(id, false);
        await session.WaitForStateAsync(id, ProfileState.Disconnected);
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.Failed);
    }

    [Fact]
    public async Task ReconnectRestartsAnEnabledProfileWithTheNewText()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.Connected);

        var result = await session.Client.UpdateProfileContentAsync(id, Fixture.OpenVpnText(markers: ["# fake: fail"]), reconnect: true);

        Assert.True(result.Profile.DesiredEnabled);
        await session.WaitForStateAsync(id, ProfileState.Failed);
    }

    [Fact]
    public async Task ReconnectLeavesADisabledProfileDisconnected()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");

        var result = await session.Client.UpdateProfileContentAsync(id, Fixture.OpenVpnText("other.example.net"), reconnect: true);

        Assert.Equal(ProfileState.Disconnected, result.Profile.State);
        Assert.False(result.Profile.DesiredEnabled);
    }

    // The real parser. The daemon has real engines on macOS only so far, and a real daemon is started only
    // when PLAITWAY_REAL_DAEMON_TESTS is set, because it would then own the machine's routes.

    private async Task<DaemonSession?> StartRealOrSkipAsync()
    {
        if (Environment.GetEnvironmentVariable("PLAITWAY_REAL_DAEMON_TESTS") != "1")
        {
            Assert.Skip("needs the daemon's real engines; set PLAITWAY_REAL_DAEMON_TESTS=1 where they exist (macOS only so far)");
        }

        return await DaemonSession.StartAsync(binary);
    }

    [Fact]
    public async Task TheRealParserNamesTheLineItRejects()
    {
        await using var session = await StartRealOrSkipAsync() ?? throw new InvalidOperationException();
        var id = await session.ImportFixtureAsync("office.ovpn");
        var broken = Fixture.OpenVpnText(extra: ["route 10.0.0.0 notamask"]);

        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileContentAsync(id, broken));

        Assert.Equal(StatusCode.InvalidArgument, error.StatusCode);
        Assert.Equal("line 6: route: \"notamask\" is not a netmask", error.Status.Detail);
        Assert.Equal(new ConfigDiagnostic(6, "route: \"notamask\" is not a netmask"), ConfigDiagnostic.FromError(error));
    }

    /// <summary>The daemon counts the lines of the text it was sent; the editor shows the masked one.</summary>
    [Fact]
    public async Task ARejectionBehindAMaskedKeyBlockIsMarkedOnTheLineTheEditorShows()
    {
        await using var session = await StartRealOrSkipAsync() ?? throw new InvalidOperationException();
        const string original = "client\ndev tun\nremote vpn.example.net 1194\n<key>\n-----BEGIN PRIVATE KEY-----\nAAAA\nBBBB\n-----END PRIVATE KEY-----\n</key>\n";
        var id = await session.ImportFixtureAsync("office.ovpn", System.Text.Encoding.UTF8.GetBytes(original));
        var mask = new SecretMask(await session.Client.GetProfileContentAsync(id), ProfileKind.Openvpn);
        Assert.Contains("<key>\n‹secret 1›\n</key>", mask.DisplayText, StringComparison.Ordinal);

        var edited = mask.DisplayText + "route 10.0.0.0 notamask\n";
        var shownLine = edited.Split('\n').Select((line, index) => (line, index)).First(pair => pair.line.StartsWith("route", StringComparison.Ordinal)).index + 1;
        var restoration = mask.Restore(edited);
        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.UpdateProfileContentAsync(id, restoration.Text));

        var reported = ConfigDiagnostic.FromError(error)!.Line!.Value;
        // The key block is four lines in the text the daemon read and one in the editor.
        Assert.Equal(shownLine + 3, reported);
        Assert.Equal(shownLine, restoration.DisplayLine(reported));
    }
}
