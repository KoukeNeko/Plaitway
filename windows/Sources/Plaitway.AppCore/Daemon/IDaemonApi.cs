using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.AppCore.Daemon;

/// <summary>
/// What the app asks of the daemon: <see cref="DaemonClient"/> without the pipe, so that the model can be
/// tested against a scripted daemon. Every call fails the way <see cref="DaemonClient"/> does, with an
/// <see cref="Grpc.Core.RpcException"/> that <see cref="DaemonFailure.From"/> classifies.
/// </summary>
public interface IDaemonApi
{
    /// <summary>The daemon's version, protocol and engines.</summary>
    Task<DaemonInfo> GetDaemonInfoAsync(CancellationToken cancellationToken = default);

    /// <summary>The network, the routes the daemon owns, the stale ones, the resolver entries and the journal.</summary>
    Task<Diagnostics> GetDiagnosticsAsync(CancellationToken cancellationToken = default);

    /// <summary>Re-reads the network and rebuilds every route and DNS entry the daemon owns.</summary>
    Task ResyncAsync(CancellationToken cancellationToken = default);

    /// <summary>Removes a route that diagnostics listed as stale.</summary>
    Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default);

    /// <summary>Stores a new profile, last in priority order.</summary>
    Task<ProfileImport> ImportProfileAsync(
        ReadOnlyMemory<byte> content, string sourceFilename, string name = "", ProfileSettings? settings = null, CancellationToken cancellationToken = default);

    /// <summary>Changes the name, the settings or both.</summary>
    Task<Profile> UpdateProfileAsync(string id, string? name = null, ProfileSettings? settings = null, CancellationToken cancellationToken = default);

    /// <summary>The stored text of a profile, secrets included.</summary>
    Task<string> GetProfileContentAsync(string id, CancellationToken cancellationToken = default);

    /// <summary>Replaces the text of a profile with the checks of an import.</summary>
    Task<ProfileImport> UpdateProfileContentAsync(string id, string content, bool reconnect = false, CancellationToken cancellationToken = default);

    /// <summary>Disconnects the profile and removes it.</summary>
    Task DeleteProfileAsync(string id, CancellationToken cancellationToken = default);

    /// <summary>Sets the priority order; <paramref name="ids"/> lists every profile, highest priority first.</summary>
    Task<IReadOnlyList<Profile>> ReorderProfilesAsync(IEnumerable<string> ids, CancellationToken cancellationToken = default);

    /// <summary>Asks the daemon to connect or disconnect a profile.</summary>
    Task<Profile> SetProfileEnabledAsync(string id, bool enabled, CancellationToken cancellationToken = default);

    /// <summary>Answers the credential request of a profile.</summary>
    Task<Profile> ProvideCredentialsAsync(string profileId, CredentialKind kind, Credentials credentials, CancellationToken cancellationToken = default);

    /// <summary>The profiles as they change; survives the daemon going away (see <see cref="DaemonClient.WatchProfilesAsync"/>).</summary>
    IAsyncEnumerable<ProfileWatchEvent> WatchProfilesAsync(CancellationToken cancellationToken = default);

    /// <summary>The buffered tail of a log and then its live lines; an empty id is the daemon's own log.</summary>
    IAsyncEnumerable<LogLine> WatchLogsAsync(string profileId, int tailLines = 0, CancellationToken cancellationToken = default);
}
