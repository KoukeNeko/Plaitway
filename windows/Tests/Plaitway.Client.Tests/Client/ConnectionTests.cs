using Plaitway.Client.Tests.Support;
using Plaitway.V1;

using static Plaitway.Client.Tests.Support.WatchEvents;

namespace Plaitway.Client.Tests.Client;

/// <summary>How long the connection to the daemon lives, and what ends it.</summary>
public sealed class ConnectionTests(DaemonBinary binary)
{
    /// <summary>
    /// The daemon (grpc-go defaults) answers a keepalive ping that comes within 5 minutes of the last with
    /// a strike, and closes the connection with GOAWAY after the third. A client that pinged every 30 s
    /// lost its watch at about 95 s and again at about 191 s; this holds a watch for longer than the
    /// first of them, which is the shortest time that shows the third strike.
    /// </summary>
    [Fact]
    [Trait("Category", "LongRunning")]
    public async Task AWatchThatStaysIdleForMinutesIsNeverCutOff()
    {
        var idle = TimeSpan.FromSeconds(155);
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
        var profile = (await client.ImportProfileAsync(Fixture.OpenVpn(), "office.ovpn")).Profile.Id;
        await DaemonSession.WaitUntilAsync("the import to be reported", () => Count<ProfileChanged>(events) >= 1);
        var seenBefore = Count<ProfileChanged>(events);

        await Task.Delay(idle);

        // The connection still carries events: it is the same stream, not a new one that the client opened again.
        await client.SetProfileEnabledAsync(profile, true);
        await DaemonSession.WaitUntilAsync("a change after the idle time", () => Count<ProfileChanged>(events) > seenBefore);
        Assert.Equal(1, Count<ProfilesSnapshot>(events));
        Assert.Equal(0, Count<DaemonUnavailable>(events));

        await stop.CancelAsync();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => watching);
    }

    [Fact]
    public async Task DisposingTheClientClosesThePipe()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        var client = DaemonClient.ConnectAs(server.Pipe, TestPipeServer.CurrentUser);
        // The server never answers, so the call is pending on an open connection.
        var pending = Task.Run(() => client.GetDaemonInfoAsync());
        await server.WaitForConnectionAsync();
        Assert.Equal(0, server.HangUps);

        await client.DisposeAsync();

        await DaemonSession.WaitUntilAsync("the server to see the client hang up", () => server.HangUps == 1, TimeSpan.FromSeconds(5));
        await Assert.ThrowsAnyAsync<Exception>(() => pending);
    }
}

