using System.Runtime.CompilerServices;
using System.Threading.Channels;
using Plaitway.AppCore.Daemon;
using Plaitway.Client;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>
/// A daemon that does one thing: it streams logs as the test scripts them. Each stream starts with the buffered tail
/// the test gives for it and then follows <see cref="Push"/> until it is cancelled, as the real one does.
/// </summary>
/// <param name="tailOfStream">The buffered tail of the stream with this number, counted from 1.</param>
internal sealed class ScriptedLogApi(Func<int, IReadOnlyList<LogLine>> tailOfStream) : IDaemonApi
{
    private readonly Channel<LogLine> _live = Channel.CreateUnbounded<LogLine>();
    private int _streamsOpened;

    /// <summary>How many log streams were opened.</summary>
    public int StreamsOpened => Volatile.Read(ref _streamsOpened);

    /// <summary>A line the daemon logs now; the open stream delivers it.</summary>
    public void Push(LogLine line) => _live.Writer.TryWrite(line);

    /// <summary>A line as the daemon logs it.</summary>
    public static LogLine Line(string text, LogLevel level = LogLevel.Info) => new() { Text = text, Level = level };

    public IAsyncEnumerable<LogLine> WatchLogsAsync(string profileId, int tailLines = 0, CancellationToken cancellationToken = default) =>
        Stream(tailOfStream(Interlocked.Increment(ref _streamsOpened)), cancellationToken);

    private async IAsyncEnumerable<LogLine> Stream(IReadOnlyList<LogLine> tail, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        foreach (var line in tail)
        {
            yield return line;
        }

        await foreach (var line in _live.Reader.ReadAllAsync(cancellationToken))
        {
            yield return line;
        }
    }

    public Task<DaemonInfo> GetDaemonInfoAsync(CancellationToken cancellationToken = default) => throw Unscripted();

    public Task<Diagnostics> GetDiagnosticsAsync(CancellationToken cancellationToken = default) => throw Unscripted();

    public Task ResyncAsync(CancellationToken cancellationToken = default) => throw Unscripted();

    public Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default) => throw Unscripted();

    public Task<ProfileImport> ImportProfileAsync(
        ReadOnlyMemory<byte> content, string sourceFilename, string name = "", ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        throw Unscripted();

    public Task<Profile> UpdateProfileAsync(string id, string? name = null, ProfileSettings? settings = null, CancellationToken cancellationToken = default) =>
        throw Unscripted();

    public Task<string> GetProfileContentAsync(string id, CancellationToken cancellationToken = default) => throw Unscripted();

    public Task<ProfileImport> UpdateProfileContentAsync(string id, string content, bool reconnect = false, CancellationToken cancellationToken = default) =>
        throw Unscripted();

    public Task DeleteProfileAsync(string id, CancellationToken cancellationToken = default) => throw Unscripted();

    public Task<IReadOnlyList<Profile>> ReorderProfilesAsync(IEnumerable<string> ids, CancellationToken cancellationToken = default) => throw Unscripted();

    public Task<Profile> SetProfileEnabledAsync(string id, bool enabled, CancellationToken cancellationToken = default) => throw Unscripted();

    public Task<Profile> ProvideCredentialsAsync(string profileId, CredentialKind kind, Credentials credentials, CancellationToken cancellationToken = default) =>
        throw Unscripted();

    public IAsyncEnumerable<ProfileWatchEvent> WatchProfilesAsync(CancellationToken cancellationToken = default) => throw Unscripted();

    private static NotSupportedException Unscripted() => new("the scripted daemon only streams logs");
}
