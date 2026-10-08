using System.Diagnostics;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Support;

/// <summary>
/// A daemon, a client connected to it, and what the profile watch has reported, so that a test can wait
/// for the daemon to say what it did instead of guessing. Everything is torn down by <see cref="DisposeAsync"/>.
/// </summary>
public sealed class DaemonSession : IAsyncDisposable
{
    private static readonly TimeSpan DefaultTimeout = TimeSpan.FromSeconds(15);
    private static readonly TimeSpan PollInterval = TimeSpan.FromMilliseconds(20);

    /// <summary>A short backoff: tests that restart the daemon should not wait for the product's.</summary>
    public static readonly BackoffPolicy FastBackoff = new(TimeSpan.FromMilliseconds(20), TimeSpan.FromMilliseconds(250), 1.6, 0.2);

    private readonly ProfileSet _profiles = new();
    private readonly object _lock = new();
    private readonly CancellationTokenSource _stop = new();
    private Task _watching = Task.CompletedTask;
    private bool _available;
    private int _snapshots;

    private DaemonSession(DaemonProcess daemon, DaemonClient client)
    {
        Daemon = daemon;
        Client = client;
    }

    /// <summary>The daemon process.</summary>
    public DaemonProcess Daemon { get; }

    /// <summary>A client of the daemon.</summary>
    public DaemonClient Client { get; }

    /// <summary>True from a snapshot of the watch until the watch reports the daemon gone.</summary>
    public bool IsAvailable
    {
        get
        {
            lock (_lock)
            {
                return _available;
            }
        }
    }

    /// <summary>How many snapshots the watch has delivered.</summary>
    public int Snapshots
    {
        get
        {
            lock (_lock)
            {
                return _snapshots;
            }
        }
    }

    /// <summary>The profiles as the watch last reported them, in priority order.</summary>
    public IReadOnlyList<Profile> Profiles
    {
        get
        {
            lock (_lock)
            {
                return [.. _profiles.Profiles];
            }
        }
    }

    /// <summary>Starts a daemon and a client, and waits for the first snapshot.</summary>
    public static async Task<DaemonSession> StartAsync(DaemonBinary binary)
    {
        var daemon = await DaemonProcess.StartAsync(binary).ConfigureAwait(false);
        var session = new DaemonSession(daemon, DaemonClient.Connect(daemon.Pipe, FastBackoff));
        session._watching = session.WatchAsync();
        try
        {
            await WaitUntilAsync("the first snapshot", () => session.IsAvailable).ConfigureAwait(false);
            return session;
        }
        catch
        {
            await session.DisposeAsync().ConfigureAwait(false);
            throw;
        }
    }

    /// <summary>The profile with <paramref name="id"/> as the watch last reported it.</summary>
    public Profile? Profile(string id)
    {
        lock (_lock)
        {
            return _profiles.Find(id);
        }
    }

    /// <summary>Imports a profile built from <see cref="Fixture"/>, waits until the watch shows it and returns its id.</summary>
    public async Task<string> ImportFixtureAsync(string filename, byte[]? content = null, ProfileSettings? settings = null)
    {
        var imported = await Client.ImportProfileAsync(content ?? Fixture.OpenVpn(), filename, settings: settings).ConfigureAwait(false);
        var id = imported.Profile.Id;
        await WaitUntilAsync($"{filename} to appear", () => Profile(id) is not null).ConfigureAwait(false);
        return id;
    }

    /// <summary>Waits until the watch reports the profile in <paramref name="state"/>.</summary>
    public Task WaitForStateAsync(string id, ProfileState state) =>
        WaitUntilAsync($"{id} to be {state}", () => Profile(id)?.State == state);

    /// <summary>Waits for <paramref name="condition"/>, polling; fails with what was waited for.</summary>
    public static async Task WaitUntilAsync(string what, Func<bool> condition, TimeSpan? timeout = null)
    {
        var clock = Stopwatch.StartNew();
        while (!condition())
        {
            if (clock.Elapsed > (timeout ?? DefaultTimeout))
            {
                throw new TimeoutException($"timed out waiting for {what}");
            }

            await Task.Delay(PollInterval).ConfigureAwait(false);
        }
    }

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        try
        {
            await _stop.CancelAsync().ConfigureAwait(false);
            await _watching.ConfigureAwait(false);
            await Client.DisposeAsync().ConfigureAwait(false);
        }
        finally
        {
            await Daemon.DisposeAsync().ConfigureAwait(false);
            _stop.Dispose();
        }
    }

    private async Task WatchAsync()
    {
        try
        {
            await foreach (var watchEvent in Client.WatchProfilesAsync(_stop.Token).ConfigureAwait(false))
            {
                lock (_lock)
                {
                    _profiles.Apply(watchEvent);
                    switch (watchEvent)
                    {
                        case ProfilesSnapshot:
                            _available = true;
                            _snapshots++;
                            break;
                        case DaemonUnavailable:
                            _available = false;
                            break;
                        default:
                            break;
                    }
                }
            }
        }
        catch (OperationCanceledException) when (_stop.IsCancellationRequested)
        {
            // The test is over: this is how the watch is told to stop.
        }
    }
}
