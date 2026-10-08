using CommunityToolkit.Mvvm.ComponentModel;
using Grpc.Core;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Presentation;
using Plaitway.V1;

namespace Plaitway.AppCore.Logs;

/// <summary>One line of a log as the page keeps it.</summary>
/// <param name="Id">Unique within the tail, rising with every line.</param>
/// <param name="Time">When the daemon logged it, or null when it did not say.</param>
/// <param name="Level">How serious it is.</param>
/// <param name="Text">What it says.</param>
public sealed record LogEntry(int Id, DateTimeOffset? Time, LogLevel Level, string Text)
{
    /// <summary>A list item is named by the text of the item it shows.</summary>
    public override string ToString() => PlainText;

    /// <summary>The line as it is copied: "12:00:01 INFO text".</summary>
    public string PlainText => string.Join(' ', new[] { Time is { } time ? Formatting.LogTime(time) : null, Level.Tag(), Text }.OfType<string>());
}

/// <summary>
/// The live tail of one log: a profile's, or the daemon's own for an empty id. Lines that arrive within a moment of
/// each other are shown together, because the buffered tail comes as one burst and a page that is drawn again for each
/// line of it takes seconds to settle. The client already reconnects and skips the lines it delivered, so a daemon
/// that restarts leaves the lines on screen. A stream begun again, as when the page is left and opened once more,
/// starts with the buffered tail once more: what is shown stays until that stream's first line arrives and is then
/// replaced by it, as in macos/Sources/PlaitwayMenuBar/LogTail.swift.
/// </summary>
/// <param name="time">The clock of the batching.</param>
public sealed partial class LogTail(TimeProvider time) : ObservableObject
{
    /// <summary>Lines kept; the daemon keeps 1000 itself, so this only bounds a long session.</summary>
    public const int Capacity = 2000;

    /// <summary>
    /// How long a new stream has to bring its first line. The daemon sends the buffered tail at once, so a stream that
    /// has said nothing by then has no tail, and the lines of the old stream are not its lines.
    /// </summary>
    public static readonly TimeSpan TailGracePeriod = TimeSpan.FromMilliseconds(300);

    /// <summary>Trimming happens in batches of this many, so that a full buffer does not shift on every line.</summary>
    private const int TrimSlack = 200;

    private static readonly TimeSpan BatchInterval = TimeSpan.FromMilliseconds(50);

    private readonly List<LogLine> _pending = [];
    private int _nextId;
    private bool _flushScheduled;

    /// <summary>The stream is new and its first line has not come: the lines shown are the old stream's.</summary>
    private bool _replacesEntries;

    /// <summary>The lines shown, oldest first. A new list on every change.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<LogEntry> Entries { get; private set; } = [];

    /// <summary>All lines, as they are copied.</summary>
    public string PlainText => string.Join('\n', Entries.Select(entry => entry.PlainText));

    /// <summary>
    /// Follows the log until <paramref name="cancellationToken"/> is cancelled. A profile that no longer exists, or that
    /// the user may not read, ends it without a word: there is nothing to show.
    /// </summary>
    public async Task RunAsync(ProfileStore store, string profileId, CancellationToken cancellationToken)
    {
        BeginStream();
        _ = ClearIfNoTailArrivesAsync(cancellationToken);
        try
        {
            await foreach (var line in store.Logs(profileId, cancellationToken).WithCancellation(cancellationToken))
            {
                Enqueue(line);
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // The page was left.
        }
        catch (RpcException error) when (error.StatusCode is StatusCode.NotFound or StatusCode.PermissionDenied)
        {
            // Not retried by the client either.
        }
    }

    /// <summary>
    /// A stream begins: the lines shown belong to the one before and are replaced when the first line of this one comes.
    /// <see cref="RunAsync"/> does this itself; it is public for a caller that feeds the tail by hand.
    /// </summary>
    public void BeginStream()
    {
        // Lines of the old stream that were not shown yet would be shown after the new one's first and repeat them.
        _pending.Clear();
        _replacesEntries = true;
    }

    /// <summary>Takes a line to be shown with the ones that come with it.</summary>
    public void Enqueue(LogLine line)
    {
        _pending.Add(line);
        if (_flushScheduled)
        {
            return;
        }

        _flushScheduled = true;
        _ = FlushAfterBatchIntervalAsync();
    }

    /// <summary>Shows a line now.</summary>
    public void Append(LogLine line) => Show([line]);

    private async Task FlushAfterBatchIntervalAsync()
    {
        await Task.Delay(BatchInterval, time);
        _flushScheduled = false;
        var lines = _pending.ToList();
        _pending.Clear();
        Show(lines);
    }

    /// <summary>A log with no lines has no tail to bring, and a stream that has brought nothing must not leave the old stream's lines standing.</summary>
    private async Task ClearIfNoTailArrivesAsync(CancellationToken cancellationToken)
    {
        try
        {
            await Task.Delay(TailGracePeriod, time, cancellationToken);
        }
        catch (OperationCanceledException)
        {
            // The page was left before the stream said anything; what is shown stays for the next stream to replace.
            return;
        }

        if (_replacesEntries && _pending.Count == 0)
        {
            Show([]);
        }
    }

    private void Show(IReadOnlyList<LogLine> lines)
    {
        var entries = new List<LogEntry>(_replacesEntries ? [] : Entries);
        _replacesEntries = false;
        entries.AddRange(lines.Select(line => new LogEntry(_nextId++, line.Time is null ? null : line.Time.ToDateTimeOffset(), line.Level, line.Text)));
        if (entries.Count > Capacity + TrimSlack)
        {
            entries.RemoveRange(0, entries.Count - Capacity);
        }

        Entries = entries;
        OnPropertyChanged(nameof(PlainText));
    }
}
