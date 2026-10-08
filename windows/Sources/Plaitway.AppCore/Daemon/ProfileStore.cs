using CommunityToolkit.Mvvm.ComponentModel;
using Microsoft.Extensions.Logging;
using Plaitway.AppCore.Platform;
using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.AppCore.Daemon;

/// <summary>Whether the daemon answers.</summary>
public enum DaemonConnection
{
    /// <summary>Nothing has been heard yet.</summary>
    Connecting,

    /// <summary>The profile watch delivered its snapshot and has not been lost since.</summary>
    Connected,

    /// <summary>The daemon is down, or refuses this user or this server; <see cref="ProfileStore.UnavailableCause"/> says which.</summary>
    Unavailable,
}

/// <summary>
/// The daemon's profiles as the UI sees them. The daemon is the only source of truth: every command sends a
/// request and the result arrives through the watch, so the store never guesses a state.
/// </summary>
/// <remarks>
/// Call <see cref="Start"/> once and <see cref="StopAsync"/> before dropping the store. Properties and commands
/// belong to the UI thread (<see cref="IUiScheduler"/>); the watch reads the pipe elsewhere and hands each event over.
/// The credential flow is in <c>ProfileStore.Credentials.cs</c>.
/// </remarks>
public sealed partial class ProfileStore(IDaemonApi api, ICredentialStore credentialStore, IUiScheduler ui, ILogger<ProfileStore> log)
    : ObservableObject, IAsyncDisposable
{
    /// <summary>How long a watch that ended on its own waits before it is started again. The client reconnects by itself, so this is for its own failures.</summary>
    private static readonly TimeSpan WatchRetryInterval = TimeSpan.FromMilliseconds(500);

    /// <summary>How many buffered lines come first in a log, which the daemon also assumes for zero.</summary>
    private const int LogTailLines = 200;

    private readonly ProfileSet _profiles = new();
    private readonly CancellationTokenSource _lifetime = new();
    private Task _watching = Task.CompletedTask;
    private bool _started;

    /// <summary>Whether the daemon answers.</summary>
    [ObservableProperty]
    public partial DaemonConnection Connection { get; private set; } = DaemonConnection.Connecting;

    /// <summary>Why the daemon is unavailable, while it is; null otherwise.</summary>
    [ObservableProperty]
    public partial DaemonFailureKind? UnavailableCause { get; private set; }

    /// <summary>
    /// Last known profiles in priority order, highest first. Kept while the daemon is unavailable so that the
    /// UI does not empty out on a reconnect. A new list on every change.
    /// </summary>
    [ObservableProperty]
    public partial IReadOnlyList<Profile> Profiles { get; private set; } = [];

    /// <summary>What the daemon said about itself on the latest connection.</summary>
    [ObservableProperty]
    public partial DaemonInfo? DaemonInfo { get; private set; }

    /// <summary>Profiles waiting for the user to type credentials. A new list on every change.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<CredentialPrompt> CredentialPrompts { get; private set; } = [];

    /// <summary>The profile with <paramref name="id"/>, or null.</summary>
    public Profile? Find(string id) => _profiles.Find(id);

    /// <summary>Starts following the daemon. Call it once, on the UI thread.</summary>
    public void Start()
    {
        if (_started)
        {
            return;
        }

        _started = true;
        _watching = Task.Run(() => WatchUntilCancelledAsync(_lifetime.Token));
    }

    /// <summary>Ends the watch and everything the store started in the background.</summary>
    public async Task StopAsync()
    {
        await _lifetime.CancelAsync().ConfigureAwait(true);
        await _watching.ConfigureAwait(true);
    }

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        await StopAsync().ConfigureAwait(true);
        _lifetime.Dispose();
    }

    // Commands. They fail with RpcException: NotFound for an unknown id, PermissionDenied for a call that needs an
    // administrator, InvalidArgument for rejected input and Unavailable when the daemon is down. DaemonFailure.From
    // turns one into something a UI can word.

    /// <summary>Asks the daemon to connect or disconnect a profile. Enabling a failed profile retries it.</summary>
    public async Task SetEnabledAsync(string profileId, bool enabled, CancellationToken cancellationToken = default) =>
        _ = await api.SetProfileEnabledAsync(profileId, enabled, cancellationToken);

    /// <summary>
    /// Stores a new profile, last in priority order. A rejected profile fails with InvalidArgument and the daemon's
    /// reason as the message.
    /// </summary>
    /// <param name="content">The profile file as text; never a path.</param>
    /// <param name="sourceFilename">A hint for the name and for detecting the kind.</param>
    /// <param name="credentials">The user name and password the profile came with; they are saved for the new profile, so that its first connection does not ask.</param>
    /// <param name="settings">The settings of the new profile; the defaults when null.</param>
    /// <param name="cancellationToken">Cancels the call.</param>
    public async Task<ProfileImport> ImportProfileAsync(
        ReadOnlyMemory<byte> content,
        string sourceFilename,
        Credentials? credentials = null,
        ProfileSettings? settings = null,
        CancellationToken cancellationToken = default)
    {
        var imported = await api.ImportProfileAsync(content, sourceFilename, settings: settings, cancellationToken: cancellationToken);
        if (credentials is not null)
        {
            await SaveCredentialsAsync(credentials, imported.Profile.Id, CredentialKind.UserPassword, cancellationToken);
        }

        return imported;
    }

    /// <summary>Changes the name, the settings or both. A connected profile keeps running with the settings it was started with.</summary>
    public async Task UpdateProfileAsync(string id, string? name = null, ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        _ = await api.UpdateProfileAsync(id, name, settings, cancellationToken);

    /// <summary>The stored text of a profile, private keys and inline certificates included. Needs an administrator.</summary>
    public Task<string> GetProfileContentAsync(string id, CancellationToken cancellationToken = default) => api.GetProfileContentAsync(id, cancellationToken);

    /// <summary>
    /// Replaces the text of a profile with the checks of an import: a rejected text fails with InvalidArgument and the
    /// daemon's reason (see <see cref="Client.Config.ConfigDiagnostic"/>).
    /// </summary>
    /// <param name="id">The profile.</param>
    /// <param name="content">The new text.</param>
    /// <param name="reconnect">Restarts an enabled profile with the new text at once; otherwise a running tunnel keeps the old text.</param>
    /// <param name="cancellationToken">Cancels the call.</param>
    public Task<ProfileImport> UpdateProfileContentAsync(string id, string content, bool reconnect = false, CancellationToken cancellationToken = default) =>
        api.UpdateProfileContentAsync(id, content, reconnect, cancellationToken);

    /// <summary>Disconnects the profile and removes it, with the credentials saved for it.</summary>
    public async Task DeleteProfileAsync(string id, CancellationToken cancellationToken = default)
    {
        await api.DeleteProfileAsync(id, cancellationToken);

        // The watch reports the removal too, but it may be down right now, and the Credential Manager would keep
        // the password of a profile that no longer exists.
        await ForgetSavedCredentialsAsync(id, cancellationToken);
    }

    /// <summary>Sets the priority order; <paramref name="ids"/> lists every profile, highest priority first.</summary>
    public async Task ReorderAsync(IEnumerable<string> ids, CancellationToken cancellationToken = default) =>
        _ = await api.ReorderProfilesAsync(ids, cancellationToken);

    /// <summary>The network, the routes the daemon owns, the stale ones, the resolver entries and the journal.</summary>
    public Task<Diagnostics> FetchDiagnosticsAsync(CancellationToken cancellationToken = default) => api.GetDiagnosticsAsync(cancellationToken);

    /// <summary>Re-reads the network and rebuilds every route and DNS entry the daemon owns.</summary>
    public Task ResyncAsync(CancellationToken cancellationToken = default) => api.ResyncAsync(cancellationToken);

    /// <summary>Removes a route that <see cref="FetchDiagnosticsAsync"/> listed as stale.</summary>
    public Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default) => api.RemoveStaleRouteAsync(key, cancellationToken);

    /// <summary>
    /// The buffered tail of a profile's log and then its live lines, until the consumer stops iterating. An empty
    /// <paramref name="profileId"/> is the daemon's own log. The sequence survives the daemon restarting.
    /// </summary>
    public IAsyncEnumerable<LogLine> Logs(string profileId, CancellationToken cancellationToken = default) =>
        api.WatchLogsAsync(profileId, LogTailLines, cancellationToken);

    // Watching

    private async Task WatchUntilCancelledAsync(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await foreach (var watchEvent in api.WatchProfilesAsync(cancellationToken).WithCancellation(cancellationToken).ConfigureAwait(false))
                {
                    await ui.RunAsync(() => Apply(watchEvent)).ConfigureAwait(false);
                }
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception error)
            {
                // The client survives the daemon going away; what ends the sequence is a bug or a message it cannot read.
                await ui.RunAsync(() => MarkUnavailable(error)).ConfigureAwait(false);
            }

            await DelayAsync(cancellationToken).ConfigureAwait(false);
        }
    }

    private static async Task DelayAsync(CancellationToken cancellationToken)
    {
        try
        {
            await Task.Delay(WatchRetryInterval, cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // The loop condition ends the watch.
        }
    }

    private void Apply(ProfileWatchEvent watchEvent)
    {
        switch (watchEvent)
        {
            case ProfilesSnapshot snapshot:
                _profiles.Apply(snapshot);
                Profiles = _profiles.Profiles;
                Connection = DaemonConnection.Connected;
                UnavailableCause = null;
                foreach (var profile in snapshot.Profiles)
                {
                    ReviewCredentialRequest(profile);
                }

                DiscardPromptsOfRemovedProfiles();
                _ = RefreshDaemonInfoAsync();
                break;
            case ProfileChanged changed:
                _profiles.Apply(changed);
                Profiles = _profiles.Profiles;
                ReviewCredentialRequest(changed.Profile);
                break;
            case ProfileRemoved removed:
                _profiles.Apply(removed);
                Profiles = _profiles.Profiles;
                ForgetCredentialsOf(removed.Id);
                break;
            case DaemonUnavailable unavailable:
                MarkUnavailable(unavailable.Cause);
                break;
            default:
                break;
        }
    }

    /// <summary>
    /// The daemon being down or refusing us (for example permission denied) looks the same to the watch, so the cause
    /// is logged once per outage.
    /// </summary>
    private void MarkUnavailable(Exception cause)
    {
        if (Connection != DaemonConnection.Unavailable)
        {
            LogMessages.DaemonUnavailable(log, cause);
        }

        Connection = DaemonConnection.Unavailable;
        UnavailableCause = DaemonFailure.From(cause).Kind;

        // Whatever the engines were asking for is gone with the daemon; the next snapshot says what is still asked.
        CredentialPrompts = [];
        _answered.Clear();
    }

    private async Task RefreshDaemonInfoAsync()
    {
        try
        {
            DaemonInfo = await api.GetDaemonInfoAsync(_lifetime.Token);
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested)
        {
            // The store is stopping.
        }
        catch (Exception error)
        {
            LogMessages.DaemonVersionUnreadable(log, error);
        }
    }
}
