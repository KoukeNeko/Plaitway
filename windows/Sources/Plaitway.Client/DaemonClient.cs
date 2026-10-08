using System.Runtime.CompilerServices;
using System.Security.Principal;
using System.Text;
using Google.Protobuf;
using Grpc.Core;
using Grpc.Net.Client;
using Plaitway.Client.Storage;
using Plaitway.Client.Transport;
using Plaitway.V1;

namespace Plaitway.Client;

/// <summary>
/// The daemon's control API over its named pipe: one method per RPC, and the two watches. Calls fail
/// with <see cref="RpcException"/> (<see cref="StatusCode.NotFound"/> for an unknown id,
/// <see cref="StatusCode.PermissionDenied"/> for a call that needs an administrator,
/// <see cref="StatusCode.InvalidArgument"/> for rejected input and <see cref="StatusCode.Unavailable"/>
/// when the daemon is down or its pipe was refused); <see cref="DaemonFailure.From"/> turns one into
/// something a UI can word.
/// </summary>
public sealed class DaemonClient : IAsyncDisposable
{
    /// <summary>Not <c>Encoding.UTF8</c>, which would replace invalid bytes: a profile text that is not UTF-8 is an error.</summary>
    private static readonly UTF8Encoding StrictUtf8 = new(encoderShouldEmitUTF8Identifier: false, throwOnInvalidBytes: true);

    private readonly GrpcChannel _channel;
    private readonly DaemonService.DaemonServiceClient _api;
    private readonly BackoffPolicy _backoff;
    private readonly Func<double> _random;

    private DaemonClient(GrpcChannel channel, BackoffPolicy backoff, Func<double> random)
    {
        _channel = channel;
        _api = new DaemonService.DaemonServiceClient(channel);
        _backoff = backoff;
        _random = random;
    }

    /// <summary>
    /// A client for the daemon on <paramref name="pipe"/>. Nothing is connected yet: the pipe is opened,
    /// and its server verified, when the first call is made.
    /// </summary>
    public static DaemonClient Connect(PipePath pipe) => Create(pipe, new PipeDialer(), BackoffPolicy.Default, Random.Shared.NextDouble);

    /// <summary>As <see cref="Connect(PipePath)"/>, with the backoff of the watches chosen.</summary>
    public static DaemonClient Connect(PipePath pipe, BackoffPolicy backoff) => Create(pipe, new PipeDialer(), backoff, Random.Shared.NextDouble);

    /// <summary>As <see cref="Connect(PipePath)"/>, as another user; for tests.</summary>
    internal static DaemonClient ConnectAs(PipePath pipe, Func<SecurityIdentifier> lookupCaller, TimeSpan? connectTimeout = null) =>
        Create(pipe, new PipeDialer(lookupCaller, connectTimeout), BackoffPolicy.Default, Random.Shared.NextDouble);

    private static DaemonClient Create(PipePath pipe, PipeDialer dialer, BackoffPolicy backoff, Func<double> random) =>
        new(DaemonChannel.Create(pipe, dialer), backoff, random);

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        await _channel.ShutdownAsync().ConfigureAwait(false);
        _channel.Dispose();
    }

    // Daemon

    /// <summary>The daemon's version, protocol and engines.</summary>
    public Task<DaemonInfo> GetDaemonInfoAsync(CancellationToken cancellationToken = default) =>
        AwaitAsync(_api.GetDaemonInfoAsync(new GetDaemonInfoRequest(), cancellationToken: cancellationToken), cancellationToken);

    /// <summary>The network, the routes the daemon owns, the stale ones, the resolver entries and the journal.</summary>
    public Task<Diagnostics> GetDiagnosticsAsync(CancellationToken cancellationToken = default) =>
        AwaitAsync(_api.GetDiagnosticsAsync(new GetDiagnosticsRequest(), cancellationToken: cancellationToken), cancellationToken);

    /// <summary>Re-reads the network and rebuilds every route and DNS entry the daemon owns.</summary>
    public async Task ResyncAsync(CancellationToken cancellationToken = default) =>
        _ = await AwaitAsync(_api.ResyncAsync(new ResyncRequest(), cancellationToken: cancellationToken), cancellationToken).ConfigureAwait(false);

    /// <summary>Removes a route that <see cref="GetDiagnosticsAsync"/> listed as stale.</summary>
    public async Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default) =>
        _ = await AwaitAsync(_api.RemoveStaleRouteAsync(new RemoveStaleRouteRequest { Key = key }, cancellationToken: cancellationToken), cancellationToken)
            .ConfigureAwait(false);

    // Profiles

    /// <summary>The profiles in priority order, highest first.</summary>
    public async Task<IReadOnlyList<Profile>> ListProfilesAsync(CancellationToken cancellationToken = default)
    {
        var response = await AwaitAsync(_api.ListProfilesAsync(new ListProfilesRequest(), cancellationToken: cancellationToken), cancellationToken).ConfigureAwait(false);
        return response.Profiles;
    }

    /// <summary>
    /// Stores a new profile, last in priority order. Needs an administrator. A rejected profile fails
    /// with <see cref="StatusCode.InvalidArgument"/> and the daemon's reason as the message.
    /// </summary>
    /// <param name="content">The profile file as text; never a path (see <see cref="Import.ProfileImporter"/>).</param>
    /// <param name="sourceFilename">A hint for the name and for detecting the kind.</param>
    /// <param name="name">The name; empty to derive it from <paramref name="sourceFilename"/>.</param>
    /// <param name="settings">The settings of the new profile; the defaults when null.</param>
    /// <param name="cancellationToken">Cancels the call.</param>
    public async Task<ProfileImport> ImportProfileAsync(
        ReadOnlyMemory<byte> content,
        string sourceFilename,
        string name = "",
        ProfileSettings? settings = null,
        CancellationToken cancellationToken = default)
    {
        var request = new ImportProfileRequest
        {
            Name = name,
            Content = ByteString.CopyFrom(content.Span),
            SourceFilename = sourceFilename,
            Settings = settings ?? new ProfileSettings(),
        };
        var response = await AwaitAsync(_api.ImportProfileAsync(request, cancellationToken: cancellationToken), cancellationToken).ConfigureAwait(false);
        return new ProfileImport(response.Profile, response.Warnings);
    }

    /// <summary>
    /// Changes the name, the settings or both. A connected profile keeps running with the settings it was
    /// started with. Needs an administrator.
    /// </summary>
    public Task<Profile> UpdateProfileAsync(
        string id, string? name = null, ProfileSettings? settings = null, CancellationToken cancellationToken = default)
    {
        var request = new UpdateProfileRequest { Id = id };
        if (name is not null)
        {
            request.Name = name;
        }

        if (settings is not null)
        {
            request.Settings = settings;
        }

        return AwaitAsync(_api.UpdateProfileAsync(request, cancellationToken: cancellationToken), cancellationToken);
    }

    /// <summary>
    /// The stored text of a profile, private keys and inline certificates included. Needs an
    /// administrator. A byte order mark the text starts with is kept.
    /// </summary>
    /// <exception cref="RpcException">The call failed, or the text is not valid UTF-8 (<see cref="StatusCode.DataLoss"/>).</exception>
    public async Task<string> GetProfileContentAsync(string id, CancellationToken cancellationToken = default)
    {
        var response = await AwaitAsync(_api.GetProfileContentAsync(new GetProfileContentRequest { Id = id }, cancellationToken: cancellationToken), cancellationToken)
            .ConfigureAwait(false);
        try
        {
            return StrictUtf8.GetString(response.Content.Span);
        }
        catch (DecoderFallbackException error)
        {
            throw new RpcException(new Status(StatusCode.DataLoss, "the profile text is not valid UTF-8", error));
        }
    }

    /// <summary>
    /// Replaces the text of a profile with the checks of an import: a rejected text fails with
    /// <see cref="StatusCode.InvalidArgument"/> and the daemon's reason (see <see cref="Config.ConfigDiagnostic"/>),
    /// and so does text of the other kind. Needs an administrator.
    /// </summary>
    /// <param name="id">The profile.</param>
    /// <param name="content">The new text.</param>
    /// <param name="reconnect">Restarts an enabled profile with the new text at once; otherwise a running tunnel keeps the old text until it connects again.</param>
    /// <param name="cancellationToken">Cancels the call.</param>
    public async Task<ProfileImport> UpdateProfileContentAsync(
        string id, string content, bool reconnect = false, CancellationToken cancellationToken = default)
    {
        var request = new UpdateProfileContentRequest { Id = id, Content = ByteString.CopyFromUtf8(content), Reconnect = reconnect };
        var response = await AwaitAsync(_api.UpdateProfileContentAsync(request, cancellationToken: cancellationToken), cancellationToken).ConfigureAwait(false);
        return new ProfileImport(response.Profile, response.Warnings);
    }

    /// <summary>Disconnects the profile and removes it. Needs an administrator.</summary>
    public async Task DeleteProfileAsync(string id, CancellationToken cancellationToken = default) =>
        _ = await AwaitAsync(_api.DeleteProfileAsync(new DeleteProfileRequest { Id = id }, cancellationToken: cancellationToken), cancellationToken).ConfigureAwait(false);

    /// <summary>Sets the priority order. <paramref name="ids"/> lists every profile, highest priority first. Needs an administrator.</summary>
    public async Task<IReadOnlyList<Profile>> ReorderProfilesAsync(IEnumerable<string> ids, CancellationToken cancellationToken = default)
    {
        var request = new ReorderProfilesRequest();
        request.Ids.AddRange(ids);
        var response = await AwaitAsync(_api.ReorderProfilesAsync(request, cancellationToken: cancellationToken), cancellationToken).ConfigureAwait(false);
        return response.Profiles;
    }

    /// <summary>Asks the daemon to connect or disconnect a profile. Enabling a failed profile retries it.</summary>
    public Task<Profile> SetProfileEnabledAsync(string id, bool enabled, CancellationToken cancellationToken = default) =>
        AwaitAsync(_api.SetProfileEnabledAsync(new SetProfileEnabledRequest { Id = id, Enabled = enabled }, cancellationToken: cancellationToken), cancellationToken);

    /// <summary>Answers the credential request of a profile that is awaiting credentials. The daemon holds the answer in memory only.</summary>
    /// <param name="profileId">The profile that asked.</param>
    /// <param name="kind">What it asked for.</param>
    /// <param name="credentials">The answer; <see cref="Credentials.Username"/> is ignored for a key passphrase.</param>
    /// <param name="cancellationToken">Cancels the call.</param>
    public Task<Profile> ProvideCredentialsAsync(
        string profileId, CredentialKind kind, Credentials credentials, CancellationToken cancellationToken = default)
    {
        var request = new ProvideCredentialsRequest
        {
            ProfileId = profileId,
            Kind = kind,
            Username = credentials.Username,
            Password = credentials.Password,
        };
        return AwaitAsync(_api.ProvideCredentialsAsync(request, cancellationToken: cancellationToken), cancellationToken);
    }

    // Watches

    /// <summary>
    /// The profiles as they change: a <see cref="ProfilesSnapshot"/> first, then one event per change.
    /// The sequence does not end when the daemon goes away. It reports <see cref="DaemonUnavailable"/>
    /// once, retries with the client's <see cref="BackoffPolicy"/>, and starts again with a snapshot
    /// when the daemon is back. It ends when <paramref name="cancellationToken"/> is cancelled (with an
    /// <see cref="OperationCanceledException"/>) or when the consumer stops iterating; the call to the
    /// daemon is cancelled either way.
    /// </summary>
    public async IAsyncEnumerable<ProfileWatchEvent> WatchProfilesAsync([EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        var stream = ReconnectingStream.ReadAsync(
            token => _api.WatchProfiles(new WatchProfilesRequest(), cancellationToken: token), _backoff, _random, cancellationToken);
        await foreach (var item in stream.WithCancellation(cancellationToken).ConfigureAwait(false))
        {
            yield return item.Outage is { } outage ? new DaemonUnavailable(outage) : ToEvent(item.Message!);
        }
    }

    private static ProfileWatchEvent ToEvent(ProfileEvent message) => message.EventCase switch
    {
        ProfileEvent.EventOneofCase.Snapshot => new ProfilesSnapshot(message.Snapshot.Profiles),
        ProfileEvent.EventOneofCase.Changed => new ProfileChanged(message.Changed),
        ProfileEvent.EventOneofCase.Removed => new ProfileRemoved(message.Removed),
        _ => throw new InvalidDataException("the daemon sent a profile event with nothing in it"),
    };

    /// <summary>
    /// The buffered tail of a profile's log and then its live lines. An empty <paramref name="profileId"/>
    /// is the daemon's own log. As with the profile watch, the sequence survives the daemon going away:
    /// it resubscribes with the client's <see cref="BackoffPolicy"/> and skips the lines it has already
    /// delivered. An unknown profile fails with <see cref="StatusCode.NotFound"/>, which is not retried.
    /// </summary>
    /// <param name="profileId">The profile, or empty for the daemon.</param>
    /// <param name="tailLines">How many buffered lines come first; 0 means the daemon's default of 200.</param>
    /// <param name="cancellationToken">Ends the sequence with an <see cref="OperationCanceledException"/>.</param>
    public async IAsyncEnumerable<LogLine> WatchLogsAsync(
        string profileId, int tailLines = 0, [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        var stream = ReconnectingStream.ReadAsync(
            token => _api.WatchLogs(new WatchLogsRequest { ProfileId = profileId, TailLines = tailLines }, cancellationToken: token),
            _backoff,
            _random,
            cancellationToken,
            isPermanent: error => error is RpcException { StatusCode: StatusCode.NotFound or StatusCode.PermissionDenied });
        await foreach (var line in WithoutRepeats(stream, cancellationToken).ConfigureAwait(false))
        {
            yield return line;
        }
    }

    /// <summary>The lines of a log stream, without those that a new stream repeats after an outage.</summary>
    internal static async IAsyncEnumerable<LogLine> WithoutRepeats(
        IAsyncEnumerable<StreamItem<LogLine>> stream, [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        var gate = new LogLineGate();
        await foreach (var item in stream.WithCancellation(cancellationToken).ConfigureAwait(false))
        {
            if (item.Message is not { } line)
            {
                gate.StreamLost();
            }
            else if (gate.Admit(line))
            {
                yield return line;
            }
        }
    }

    /// <summary>
    /// Waits for the reply and disposes the call. gRPC reports a call that the caller cancelled as an
    /// <see cref="RpcException"/>; it is an <see cref="OperationCanceledException"/> here, as for any other awaited work.
    /// </summary>
    private static async Task<T> AwaitAsync<T>(AsyncUnaryCall<T> call, CancellationToken cancellationToken)
    {
        using (call)
        {
            try
            {
                return await call.ResponseAsync.ConfigureAwait(false);
            }
            catch (RpcException) when (cancellationToken.IsCancellationRequested)
            {
                throw new OperationCanceledException(cancellationToken);
            }
        }
    }
}
