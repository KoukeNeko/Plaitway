using System.Diagnostics;
using Plaitway.Client.Tests.Support;
using Plaitway.Client.Transport;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Client;

/// <summary>
/// The two watches end when their consumer says so, and the call to the daemon ends with them: the
/// daemon logs a cancelled call with <c>code=Canceled</c> (at debug level, for the read-only calls).
/// </summary>
public sealed class WatchCancellationTests(DaemonBinary binary)
{
    private static readonly TimeSpan Patience = TimeSpan.FromSeconds(10);

    private static Task WaitForCancelledCallAsync(DaemonProcess daemon, string method) =>
        DaemonSession.WaitUntilAsync(
            $"the daemon to log a cancelled {method}",
            () => daemon.Log.Split('\n').Any(line => line.Contains($"/{method}", StringComparison.Ordinal) && line.Contains("code=Canceled", StringComparison.Ordinal)),
            Patience);

    [Fact]
    public async Task CancellingTheTokenEndsTheProfileWatchAndTheCallAtTheDaemon()
    {
        await using var daemon = await DaemonProcess.StartAsync(binary);
        await using var client = DaemonClient.Connect(daemon.Pipe, DaemonSession.FastBackoff);
        using var cancel = new CancellationTokenSource();
        await using var events = client.WatchProfilesAsync(cancel.Token).GetAsyncEnumerator(cancel.Token);
        Assert.True(await events.MoveNextAsync());
        Assert.IsType<ProfilesSnapshot>(events.Current);

        var waiting = events.MoveNextAsync().AsTask();
        await cancel.CancelAsync();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => waiting);
        await WaitForCancelledCallAsync(daemon, "WatchProfiles");
    }

    [Fact]
    public async Task StoppingToIterateEndsTheProfileWatchAndTheCallAtTheDaemon()
    {
        await using var daemon = await DaemonProcess.StartAsync(binary);
        await using var client = DaemonClient.Connect(daemon.Pipe, DaemonSession.FastBackoff);

        await foreach (var watchEvent in client.WatchProfilesAsync())
        {
            Assert.IsType<ProfilesSnapshot>(watchEvent);
            break;
        }

        await WaitForCancelledCallAsync(daemon, "WatchProfiles");
    }

    [Fact]
    public async Task CancellingTheTokenEndsTheLogWatchAndTheCallAtTheDaemon()
    {
        await using var daemon = await DaemonProcess.StartAsync(binary);
        await using var client = DaemonClient.Connect(daemon.Pipe, DaemonSession.FastBackoff);
        using var cancel = new CancellationTokenSource();
        await using var lines = client.WatchLogsAsync(string.Empty, cancellationToken: cancel.Token).GetAsyncEnumerator(cancel.Token);
        Assert.True(await lines.MoveNextAsync());

        // Read to the end of the buffered tail so that the next read waits for a live line.
        var waiting = Task.Run(async () =>
        {
            while (await lines.MoveNextAsync())
            {
            }
        });
        await Task.Delay(TimeSpan.FromMilliseconds(500));
        await cancel.CancelAsync();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => waiting);
        await WaitForCancelledCallAsync(daemon, "WatchLogs");
    }

    [Fact]
    public async Task CancellingWhileTheDaemonIsAwayEndsTheWatchPromptly()
    {
        var missing = PipePath.Parse(PipePath.Namespace + "plaitway-cs-test-absent-" + Guid.NewGuid().ToString("N")[..8]);
        await using var client = DaemonClient.ConnectAs(missing, TestPipeServer.CurrentUser, TimeSpan.FromMilliseconds(200));
        using var cancel = new CancellationTokenSource(TimeSpan.FromMilliseconds(300));
        var clock = Stopwatch.StartNew();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(async () =>
        {
            await foreach (var watchEvent in client.WatchProfilesAsync(cancel.Token))
            {
                Assert.IsType<DaemonUnavailable>(watchEvent);
            }
        });

        Assert.True(clock.Elapsed < TimeSpan.FromSeconds(3), $"took {clock.Elapsed}");
    }

    [Fact]
    public async Task TheLogWatchSurvivesARestartOfTheDaemonWithoutRepeatingLines()
    {
        await using var daemon = await DaemonProcess.StartAsync(binary);
        await using var client = DaemonClient.Connect(daemon.Pipe, DaemonSession.FastBackoff);
        var seen = new List<LogLine>();
        using var stop = new CancellationTokenSource();
        var reading = Task.Run(async () =>
        {
            await foreach (var line in client.WatchLogsAsync(string.Empty, cancellationToken: stop.Token))
            {
                lock (seen)
                {
                    seen.Add(line);
                }
            }
        });

        await DaemonSession.WaitUntilAsync("the first listening line", () => CountListening(seen) == 1);
        daemon.Kill();
        await daemon.StartAsync();
        await DaemonSession.WaitUntilAsync("the listening line of the new daemon", () => CountListening(seen) == 2);

        lock (seen)
        {
            // The new daemon starts a new log; no line is delivered twice.
            Assert.Equal(seen.Count, seen.Select(line => (line.Time.Seconds, line.Time.Nanos, line.Text)).Distinct().Count());
        }

        await stop.CancelAsync();
        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => reading);
    }

    private static int CountListening(List<LogLine> seen)
    {
        lock (seen)
        {
            return seen.Count(line => line.Text.Contains("listening", StringComparison.Ordinal));
        }
    }
}
