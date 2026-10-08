using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.AppCore.Daemon;

/// <summary>
/// <see cref="IDaemonApi"/> over a <see cref="DaemonClient"/>. It adds nothing: the client is sealed and
/// belongs to the client library, so this is the one place that forwards each call.
/// </summary>
/// <param name="client">The client to forward to; it is disposed with this object.</param>
public sealed class DaemonClientApi(DaemonClient client) : IDaemonApi, IAsyncDisposable
{
    /// <inheritdoc />
    public ValueTask DisposeAsync() => client.DisposeAsync();

    /// <inheritdoc />
    public Task<DaemonInfo> GetDaemonInfoAsync(CancellationToken cancellationToken = default) => client.GetDaemonInfoAsync(cancellationToken);

    /// <inheritdoc />
    public Task<Diagnostics> GetDiagnosticsAsync(CancellationToken cancellationToken = default) => client.GetDiagnosticsAsync(cancellationToken);

    /// <inheritdoc />
    public Task ResyncAsync(CancellationToken cancellationToken = default) => client.ResyncAsync(cancellationToken);

    /// <inheritdoc />
    public Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default) => client.RemoveStaleRouteAsync(key, cancellationToken);

    /// <inheritdoc />
    public Task<ProfileImport> ImportProfileAsync(
        ReadOnlyMemory<byte> content, string sourceFilename, string name = "", ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        client.ImportProfileAsync(content, sourceFilename, name, settings, cancellationToken);

    /// <inheritdoc />
    public Task<Profile> UpdateProfileAsync(string id, string? name = null, ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        client.UpdateProfileAsync(id, name, settings, cancellationToken);

    /// <inheritdoc />
    public Task<string> GetProfileContentAsync(string id, CancellationToken cancellationToken = default) => client.GetProfileContentAsync(id, cancellationToken);

    /// <inheritdoc />
    public Task<ProfileImport> UpdateProfileContentAsync(string id, string content, bool reconnect = false, CancellationToken cancellationToken = default) =>
        client.UpdateProfileContentAsync(id, content, reconnect, cancellationToken);

    /// <inheritdoc />
    public Task DeleteProfileAsync(string id, CancellationToken cancellationToken = default) => client.DeleteProfileAsync(id, cancellationToken);

    /// <inheritdoc />
    public Task<IReadOnlyList<Profile>> ReorderProfilesAsync(IEnumerable<string> ids, CancellationToken cancellationToken = default) =>
        client.ReorderProfilesAsync(ids, cancellationToken);

    /// <inheritdoc />
    public Task<Profile> SetProfileEnabledAsync(string id, bool enabled, CancellationToken cancellationToken = default) =>
        client.SetProfileEnabledAsync(id, enabled, cancellationToken);

    /// <inheritdoc />
    public Task<Profile> ProvideCredentialsAsync(string profileId, CredentialKind kind, Credentials credentials, CancellationToken cancellationToken = default) =>
        client.ProvideCredentialsAsync(profileId, kind, credentials, cancellationToken);

    /// <inheritdoc />
    public IAsyncEnumerable<ProfileWatchEvent> WatchProfilesAsync(CancellationToken cancellationToken = default) => client.WatchProfilesAsync(cancellationToken);

    /// <inheritdoc />
    public IAsyncEnumerable<LogLine> WatchLogsAsync(string profileId, int tailLines = 0, CancellationToken cancellationToken = default) =>
        client.WatchLogsAsync(profileId, tailLines, cancellationToken);
}
