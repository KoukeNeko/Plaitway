using Plaitway.AppCore.Daemon;
using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>
/// Passes every call on to the real client and writes down its name, so that a test can say what a view model asked the
/// daemon and, as important, what it did not.
/// </summary>
/// <param name="inner">The client.</param>
internal sealed class RecordingDaemonApi(IDaemonApi inner) : IDaemonApi
{
    private readonly List<string> _calls = [];

    /// <summary>The names of the calls so far, in order. A stream counts once, when it is opened.</summary>
    public IReadOnlyList<string> Calls
    {
        get
        {
            lock (_calls)
            {
                return [.. _calls];
            }
        }
    }

    public Task<DaemonInfo> GetDaemonInfoAsync(CancellationToken cancellationToken = default) => Record(nameof(GetDaemonInfoAsync), inner.GetDaemonInfoAsync(cancellationToken));

    public Task<Diagnostics> GetDiagnosticsAsync(CancellationToken cancellationToken = default) => Record(nameof(GetDiagnosticsAsync), inner.GetDiagnosticsAsync(cancellationToken));

    public Task ResyncAsync(CancellationToken cancellationToken = default) => Record(nameof(ResyncAsync), inner.ResyncAsync(cancellationToken));

    public Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default) =>
        Record(nameof(RemoveStaleRouteAsync), inner.RemoveStaleRouteAsync(key, cancellationToken));

    public Task<ProfileImport> ImportProfileAsync(
        ReadOnlyMemory<byte> content, string sourceFilename, string name = "", ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        Record(nameof(ImportProfileAsync), inner.ImportProfileAsync(content, sourceFilename, name, settings, cancellationToken));

    public Task<Profile> UpdateProfileAsync(string id, string? name = null, ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        Record(nameof(UpdateProfileAsync), inner.UpdateProfileAsync(id, name, settings, cancellationToken));

    public Task<string> GetProfileContentAsync(string id, CancellationToken cancellationToken = default) =>
        Record(nameof(GetProfileContentAsync), inner.GetProfileContentAsync(id, cancellationToken));

    public Task<ProfileImport> UpdateProfileContentAsync(string id, string content, bool reconnect = false, CancellationToken cancellationToken = default) =>
        Record(nameof(UpdateProfileContentAsync), inner.UpdateProfileContentAsync(id, content, reconnect, cancellationToken));

    public Task DeleteProfileAsync(string id, CancellationToken cancellationToken = default) =>
        Record(nameof(DeleteProfileAsync), inner.DeleteProfileAsync(id, cancellationToken));

    public Task<IReadOnlyList<Profile>> ReorderProfilesAsync(IEnumerable<string> ids, CancellationToken cancellationToken = default) =>
        Record(nameof(ReorderProfilesAsync), inner.ReorderProfilesAsync(ids, cancellationToken));

    public Task<Profile> SetProfileEnabledAsync(string id, bool enabled, CancellationToken cancellationToken = default) =>
        Record(nameof(SetProfileEnabledAsync), inner.SetProfileEnabledAsync(id, enabled, cancellationToken));

    public Task<Profile> ProvideCredentialsAsync(string profileId, CredentialKind kind, Credentials credentials, CancellationToken cancellationToken = default) =>
        Record(nameof(ProvideCredentialsAsync), inner.ProvideCredentialsAsync(profileId, kind, credentials, cancellationToken));

    public IAsyncEnumerable<ProfileWatchEvent> WatchProfilesAsync(CancellationToken cancellationToken = default)
    {
        Note(nameof(WatchProfilesAsync));
        return inner.WatchProfilesAsync(cancellationToken);
    }

    public IAsyncEnumerable<LogLine> WatchLogsAsync(string profileId, int tailLines = 0, CancellationToken cancellationToken = default)
    {
        Note(nameof(WatchLogsAsync));
        return inner.WatchLogsAsync(profileId, tailLines, cancellationToken);
    }

    private Task<T> Record<T>(string name, Task<T> call)
    {
        Note(name);
        return call;
    }

    private Task Record(string name, Task call)
    {
        Note(name);
        return call;
    }

    private void Note(string name)
    {
        lock (_calls)
        {
            _calls.Add(name);
        }
    }
}
