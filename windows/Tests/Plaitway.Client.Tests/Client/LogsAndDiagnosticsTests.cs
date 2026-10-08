using Grpc.Core;
using Plaitway.Client.Tests.Support;
using Plaitway.Client.Transport;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Client;

/// <summary>macos/Tests/PlaitwayTests/Client/LogsAndDiagnosticsTests.swift against the real daemon.</summary>
public sealed class LogsAndDiagnosticsTests(DaemonBinary binary)
{
    private static readonly TimeSpan LineTimeout = TimeSpan.FromSeconds(10);

    /// <summary>The first line that satisfies <paramref name="predicate"/>; fails when the stream ends or the time runs out.</summary>
    internal static async Task<LogLine> FirstLineAsync(IAsyncEnumerable<LogLine> lines, Func<LogLine, bool> predicate)
    {
        using var timeout = new CancellationTokenSource(LineTimeout);
        try
        {
            await foreach (var line in lines.WithCancellation(timeout.Token))
            {
                if (predicate(line))
                {
                    return line;
                }
            }
        }
        catch (OperationCanceledException) when (timeout.IsCancellationRequested)
        {
            throw new TimeoutException("timed out waiting for a log line");
        }

        throw new InvalidOperationException("the log stream ended without a matching line");
    }

    // logs

    [Fact]
    public async Task TailsAProfilesLog()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        await session.Client.SetProfileEnabledAsync(id, true);

        using var stop = new CancellationTokenSource();
        var line = await FirstLineAsync(session.Client.WatchLogsAsync(id, cancellationToken: stop.Token), candidate => candidate.Text.StartsWith("connected on", StringComparison.Ordinal));

        Assert.Equal(id, line.ProfileId);
        Assert.Equal(LogLevel.Info, line.Level);
        Assert.NotNull(line.Time);
    }

    [Fact]
    public async Task StreamsLiveLinesAfterTheBufferedTail()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.Connected);

        var lines = session.Client.WatchLogsAsync(id).GetAsyncEnumerator();
        await using (lines)
        {
            Assert.True(await Next(lines, line => line.Text.StartsWith("connected on", StringComparison.Ordinal)));

            // A line that did not exist when the stream was opened.
            await session.Client.SetProfileEnabledAsync(id, false);
            Assert.True(await Next(lines, line => line.Text == "stopping" && line.ProfileId == id));
        }
    }

    private static async Task<bool> Next(IAsyncEnumerator<LogLine> lines, Func<LogLine, bool> predicate)
    {
        using var timeout = new CancellationTokenSource(LineTimeout);
        while (await lines.MoveNextAsync().AsTask().WaitAsync(timeout.Token))
        {
            if (predicate(lines.Current))
            {
                return true;
            }
        }

        return false;
    }

    /// <summary>
    /// The daemon logs several lines within one tick of its clock, so lines share a time. Each of 30
    /// enable/disable cycles of a fake profile logs a burst; the client must deliver exactly what the daemon
    /// sent, which a second, unfiltered call to the same log tells.
    /// </summary>
    [Fact]
    public async Task DeliversEveryLineTheDaemonSendsExactlyOnce()
    {
        const int cycles = 30;
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        var sent = new List<LogLine>();
        var delivered = new List<LogLine>();
        using var stop = new CancellationTokenSource();
        var readingSent = Task.Run(() => ReadRawLogAsync(session.Daemon, id, sent, stop.Token));
        var readingDelivered = Task.Run(async () =>
        {
            await foreach (var line in session.Client.WatchLogsAsync(id, cancellationToken: stop.Token))
            {
                lock (delivered)
                {
                    delivered.Add(line);
                }
            }
        });

        for (var cycle = 0; cycle < cycles; cycle++)
        {
            await session.Client.SetProfileEnabledAsync(id, true);
            await session.Client.SetProfileEnabledAsync(id, false);
        }

        await DaemonSession.WaitUntilAsync(
            "both reads to have the same lines",
            () => Snapshot(sent).Count >= cycles * 2 && Snapshot(sent).Count == Snapshot(delivered).Count);
        // A repeat would arrive a moment after the last line the daemon sent.
        await Task.Delay(TimeSpan.FromMilliseconds(500));
        var expected = Snapshot(sent);
        Assert.Equal(expected.Select(Describe), Snapshot(delivered).Select(Describe));
        Assert.Equal(cycles, expected.Count(line => line.Text == "starting"));
        Assert.Contains(
            expected.Zip(expected.Skip(1)),
            pair => pair.First.Time.Equals(pair.Second.Time));

        await stop.CancelAsync();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => readingDelivered);
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => readingSent);
    }

    private static List<LogLine> Snapshot(List<LogLine> lines)
    {
        lock (lines)
        {
            return [.. lines];
        }
    }

    private static string Describe(LogLine line) => $"{line.Time.Seconds}.{line.Time.Nanos:D9} {line.Level} {line.Text}";

    /// <summary>The log as the daemon sends it: one call, no filter.</summary>
    private static async Task ReadRawLogAsync(DaemonProcess daemon, string profileId, List<LogLine> lines, CancellationToken cancellationToken)
    {
        using var channel = DaemonChannel.Create(daemon.Pipe, new PipeDialer());
        var call = new DaemonService.DaemonServiceClient(channel).WatchLogs(new WatchLogsRequest { ProfileId = profileId }, cancellationToken: cancellationToken);
        try
        {
            while (await call.ResponseStream.MoveNext(cancellationToken).ConfigureAwait(false))
            {
                lock (lines)
                {
                    lines.Add(call.ResponseStream.Current);
                }
            }
        }
        catch (RpcException) when (cancellationToken.IsCancellationRequested)
        {
            throw new OperationCanceledException(cancellationToken);
        }
    }

    [Fact]
    public async Task TailsTheDaemonsOwnLogWithAnEmptyProfileId()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var line = await FirstLineAsync(session.Client.WatchLogsAsync(string.Empty), candidate => candidate.Text.Contains("listening", StringComparison.Ordinal));

        Assert.Empty(line.ProfileId);
    }

    [Fact]
    public async Task ALogOfAnUnknownProfileIsNotFound()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        var error = await Assert.ThrowsAsync<RpcException>(() => FirstLineAsync(session.Client.WatchLogsAsync("nope"), _ => true));

        Assert.Equal(StatusCode.NotFound, error.StatusCode);
    }

    // diagnostics

    [Fact]
    public async Task DescribesTheNetworkAndTheStaleRoute()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var info = await session.Client.GetDaemonInfoAsync();

        var diagnostics = await session.Client.GetDiagnosticsAsync();

        Assert.Equal("192.168.0.1", diagnostics.Network.DefaultGatewayV4);
        Assert.Equal("en0", diagnostics.Network.DefaultInterfaceV4);
        Assert.Contains("en0", diagnostics.Network.Interfaces);
        Assert.Empty(diagnostics.OwnedRoutes);
        Assert.Equal(info.Version, diagnostics.Daemon.Version);
        var stale = Assert.Single(diagnostics.StaleRoutes);
        Assert.Equal("fake-stale-1", stale.Key);
        Assert.Equal("203.0.113.9/32", stale.Prefix);
        Assert.Equal("192.168.0.254", stale.Gateway);
        Assert.Equal("en0", stale.Interface);
        Assert.NotEmpty(stale.Reason);
    }

    [Fact]
    public async Task ListsTheRoutesTheDaemonOwnsWithTheirStates()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn", Fixture.OpenVpn(markers: ["# fake: conflict"]));
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.Connected);

        var diagnostics = await session.Client.GetDiagnosticsAsync();

        var states = diagnostics.OwnedRoutes.ToDictionary(route => route.Prefix);
        Assert.Equal(RouteState.Installed, states["192.168.1.0/24"].State);
        Assert.Equal(RouteKind.Tunnel, states["192.168.1.0/24"].Kind);
        Assert.Equal(id, states["192.168.1.0/24"].Owner);
        Assert.Equal(RouteState.Shadowed, states["10.99.0.0/16"].State);
        Assert.Equal(RouteState.Blocked, states["192.168.0.0/24"].State);
        Assert.Contains(diagnostics.ResolverEntries, entry => entry.Contains(id, StringComparison.Ordinal));

        // The profile's own view of the same routes names who shadows what.
        var routes = session.Profile(id)!.Status.Routes;
        var shadowed = routes.Single(route => route.Prefix == "10.99.0.0/16");
        Assert.Equal(RouteState.Shadowed, shadowed.State);
        Assert.Equal("fake-peer", shadowed.ShadowedBy);
        Assert.Equal(RouteState.Blocked, routes.Single(route => route.Prefix == "192.168.0.0/24").State);
    }

    [Fact]
    public async Task RemovesAStaleRouteOnce()
    {
        await using var session = await DaemonSession.StartAsync(binary);

        await session.Client.RemoveStaleRouteAsync("fake-stale-1");
        Assert.Empty((await session.Client.GetDiagnosticsAsync()).StaleRoutes);

        var error = await Assert.ThrowsAsync<RpcException>(() => session.Client.RemoveStaleRouteAsync("fake-stale-1"));
        Assert.Equal(StatusCode.NotFound, error.StatusCode);
    }

    [Fact]
    public async Task ResyncRebuildsWhatTheDaemonOwns()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("office.ovpn");
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.Connected);
        await session.Client.RemoveStaleRouteAsync("fake-stale-1");

        await session.Client.ResyncAsync();

        // The fake daemon reports the stale route again after a resync, and the connected profile reconnects for a moment.
        var diagnostics = await session.Client.GetDiagnosticsAsync();
        Assert.Equal("manual", diagnostics.Network.LastChangeReason);
        Assert.Equal(["fake-stale-1"], diagnostics.StaleRoutes.Select(route => route.Key));
        await session.WaitForStateAsync(id, ProfileState.Reconnecting);
        await session.WaitForStateAsync(id, ProfileState.Connected);
    }
}
