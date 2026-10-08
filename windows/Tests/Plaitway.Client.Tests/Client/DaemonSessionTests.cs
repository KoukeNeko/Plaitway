using Grpc.Core;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

using static Plaitway.Client.Tests.Support.WatchEvents;

[assembly: AssemblyFixture(typeof(DaemonBinary))]

namespace Plaitway.Client.Tests.Client;

/// <summary>macos/Tests/PlaitwayTests/Client/ProfileStoreTests.swift against the real daemon, through <see cref="DaemonClient"/>.</summary>
public sealed class DaemonSessionTests(DaemonBinary binary)
{
    [Fact]
    public async Task StartsEmptyAndKnowsTheDaemon()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        Assert.Empty(session.Profiles);
        var info = await session.Client.GetDaemonInfoAsync();
        Assert.True(info.Privileged);
        Assert.Equal([true, true], info.Engines.Select(engine => engine.Available));
        Assert.NotEmpty(info.Version);
    }

    [Fact]
    public async Task ConnectsSeveralProfilesAtTheSameTime()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var office = await session.ImportFixtureAsync("office.ovpn");
        var lab = await session.ImportFixtureAsync("lab.ovpn");
        var home = await session.ImportFixtureAsync("home.conf", Fixture.WireGuard());

        await Task.WhenAll(
            session.Client.SetProfileEnabledAsync(office, true),
            session.Client.SetProfileEnabledAsync(lab, true),
            session.Client.SetProfileEnabledAsync(home, true));

        await DaemonSession.WaitUntilAsync(
            "three connected profiles", () => new[] { office, lab, home }.All(id => session.Profile(id)?.State == ProfileState.Connected));
        Assert.StartsWith("utun", session.Profile(home)!.Status.InterfaceName, StringComparison.Ordinal);
    }

    [Fact]
    public async Task ReportsFailureWithAnError()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var broken = await session.ImportFixtureAsync("broken.ovpn", Fixture.OpenVpn(markers: ["# fake: fail"]));

        await session.Client.SetProfileEnabledAsync(broken, true);
        await session.WaitForStateAsync(broken, ProfileState.Failed);

        Assert.NotEmpty(session.Profile(broken)!.LastError);
    }

    [Fact]
    public async Task DisablingDisconnects()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var office = await session.ImportFixtureAsync("office.ovpn");

        await session.Client.SetProfileEnabledAsync(office, true);
        await session.WaitForStateAsync(office, ProfileState.Connected);
        await session.Client.SetProfileEnabledAsync(office, false);
        await session.WaitForStateAsync(office, ProfileState.Disconnected);

        Assert.False(session.Profile(office)!.DesiredEnabled);
    }

    [Fact]
    public async Task AnUnknownProfileIsNotFound()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.SetProfileEnabledAsync("nope", true));

        Assert.Equal(StatusCode.NotFound, error.StatusCode);
        Assert.Equal(DaemonFailureKind.NotFound, DaemonFailure.From(error).Kind);
    }

    [Fact]
    public async Task ListsTheProfilesInPriorityOrder()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var first = await session.ImportFixtureAsync("a.ovpn");
        var second = await session.ImportFixtureAsync("b.conf", Fixture.WireGuard());

        var listed = await session.Client.ListProfilesAsync();

        Assert.Equal([first, second], listed.Select(profile => profile.Id));
    }

    [Fact]
    public async Task RecoversWhenTheDaemonRestarts()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var office = await session.ImportFixtureAsync("office.ovpn");
        await session.Client.SetProfileEnabledAsync(office, true);
        await session.WaitForStateAsync(office, ProfileState.Connected);
        var snapshots = session.Snapshots;

        session.Daemon.Kill();
        await DaemonSession.WaitUntilAsync("the outage to be noticed", () => !session.IsAvailable);
        // The list keeps the last known profiles instead of emptying the UI.
        Assert.Equal(ProfileState.Connected, session.Profile(office)?.State);

        await session.Daemon.StartAsync();
        await DaemonSession.WaitUntilAsync("the watch to reconnect", () => session.IsAvailable && session.Snapshots > snapshots);
        // The profile survives in the state directory; its tunnel does not.
        Assert.Equal(ProfileState.Disconnected, session.Profile(office)?.State);
        Assert.False(session.Profile(office)!.DesiredEnabled);
    }

    /// <summary>The watch tells the outage once, however many times it retries.</summary>
    [Fact]
    public async Task ReportsAnOutageOnceAndAFreshSnapshotWhenTheDaemonIsBack()
    {
        await using var daemon = await DaemonProcess.StartAsync(binary);
        await using var client = DaemonClient.Connect(daemon.Pipe, DaemonSession.FastBackoff);
        var events = new List<ProfileWatchEvent>();
        using var stop = new CancellationTokenSource();
        var watching = Task.Run(async () =>
        {
            await foreach (var watchEvent in client.WatchProfilesAsync(stop.Token))
            {
                lock (events)
                {
                    events.Add(watchEvent);
                }
            }
        });

        await DaemonSession.WaitUntilAsync("the first snapshot", () => Count<ProfilesSnapshot>(events) == 1);
        daemon.Kill();
        await DaemonSession.WaitUntilAsync("the outage", () => Count<DaemonUnavailable>(events) == 1);
        // Several retries fail while the daemon is down (each waits for the pipe); none is reported again.
        await Task.Delay(TimeSpan.FromSeconds(2.5));
        Assert.Equal(1, Count<DaemonUnavailable>(events));

        await daemon.StartAsync();
        await DaemonSession.WaitUntilAsync("a snapshot after the restart", () => Count<ProfilesSnapshot>(events) == 2);
        Assert.IsType<ProfilesSnapshot>(Last(events));

        await stop.CancelAsync();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => watching);
    }

    private static ProfileWatchEvent Last(List<ProfileWatchEvent> events)
    {
        lock (events)
        {
            return events[^1];
        }
    }

    [Fact]
    public async Task LaunchingTheAppDoesNotReconnectWhatTheUserDisconnected()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var autoConnect = new ProfileSettings { AutoConnect = true };
        var flagged = await session.ImportFixtureAsync("flagged.ovpn", settings: autoConnect);
        // The daemon connects auto-connect profiles when it starts; once the user disconnects one, it stays off.
        Assert.Equal(ProfileState.Disconnected, session.Profile(flagged)?.State);

        // The app's own client, as at launch.
        await using var launched = DaemonClient.Connect(session.Daemon.Pipe, DaemonSession.FastBackoff);
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(10));
        var states = new List<(ProfileState State, bool Desired)>();
        await foreach (var watchEvent in launched.WatchProfilesAsync(timeout.Token))
        {
            if (watchEvent is ProfilesSnapshot snapshot)
            {
                states.AddRange(snapshot.Profiles.Select(profile => (profile.State, profile.DesiredEnabled)));
                break;
            }
        }

        await Task.Delay(TimeSpan.FromMilliseconds(500));
        var listed = await launched.ListProfilesAsync();
        Assert.Equal([(ProfileState.Disconnected, false)], states);
        Assert.Equal(ProfileState.Disconnected, listed.Single().State);
        Assert.False(listed.Single().DesiredEnabled);
    }
}

/// <summary>macos/Tests/PlaitwayTests/Client/ProfileStoreTests.swift: <c>DaemonFailureTests</c> and <c>DaemonLocationTests</c>.</summary>
public sealed class DaemonFailureTests
{
    public static TheoryData<StatusCode, DaemonFailureKind> Mapping => new()
    {
        { StatusCode.PermissionDenied, DaemonFailureKind.PermissionDenied },
        { StatusCode.Unavailable, DaemonFailureKind.Unavailable },
        { StatusCode.NotFound, DaemonFailureKind.NotFound },
        { StatusCode.InvalidArgument, DaemonFailureKind.Rejected },
        { StatusCode.FailedPrecondition, DaemonFailureKind.Rejected },
        { StatusCode.Internal, DaemonFailureKind.Other },
    };

    [Theory]
    [MemberData(nameof(Mapping))]
    public void MapsStatusCodes(StatusCode code, DaemonFailureKind kind) =>
        Assert.Equal(new DaemonFailure(kind, "reason"), DaemonFailure.From(new RpcException(new Status(code, "reason"))));

    [Fact]
    public void WrapsForeignErrors() =>
        Assert.Equal(DaemonFailureKind.Other, DaemonFailure.From(new InvalidOperationException("boom")).Kind);
}
