using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Logs;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>How much of a log to show: debug lines are most of what OpenVPN writes and hide the rest.</summary>
public enum LogFilter
{
    /// <summary>Every line.</summary>
    All,

    /// <summary>Everything but debug.</summary>
    Info,

    /// <summary>Warnings and errors.</summary>
    Warnings,

    /// <summary>Errors only.</summary>
    Errors,
}

/// <summary>The words and the rule of each <see cref="LogFilter"/>.</summary>
public static class LogFilters
{
    /// <summary>The filters in the order the picker lists them.</summary>
    public static IReadOnlyList<LogFilter> All { get; } = Enum.GetValues<LogFilter>();

    /// <summary>The label of the filter.</summary>
    public static string Label(this LogFilter filter, UiText text) => filter switch
    {
        LogFilter.All => text.AllLevels,
        LogFilter.Info => text.InfoAndAbove,
        LogFilter.Warnings => text.WarningsAndErrors,
        _ => text.ErrorsOnly,
    };

    /// <summary>Whether a line of <paramref name="level"/> passes the filter.</summary>
    public static bool Includes(this LogFilter filter, LogLevel level) => filter switch
    {
        LogFilter.All => true,
        LogFilter.Info => level != LogLevel.Debug,
        LogFilter.Warnings => level is LogLevel.Warn or LogLevel.Error,
        _ => level == LogLevel.Error,
    };
}

/// <summary>A filter and its label, for a picker.</summary>
/// <param name="Filter">The filter.</param>
/// <param name="Label">Its words.</param>
public sealed record LogFilterChoice(LogFilter Filter, string Label);

/// <summary>
/// A live log: a profile's, or the daemon's own for an empty id. It follows the newest line while the view is scrolled
/// to the end and stays where it is once the user scrolls up to read.
/// </summary>
public sealed partial class LogsViewModel : ObservableObject, IPageLifecycle, IDisposable
{
    private readonly AppModel _model;
    private readonly string _profileId;
    private readonly IClipboard _clipboard;
    private readonly LogTail _tail;
    private CancellationTokenSource? _following;
    private Task _followingTask = Task.CompletedTask;

    /// <summary>Makes the log of a profile, or of the daemon for an empty <paramref name="profileId"/>.</summary>
    public LogsViewModel(AppModel model, string profileId, IClipboard clipboard)
    {
        _model = model;
        _profileId = profileId;
        _clipboard = clipboard;
        _tail = new LogTail(model.Time);
        _tail.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(LogTail.Entries))
            {
                Refilter();
            }
        };
        Choices = [.. LogFilters.All.Select(filter => new LogFilterChoice(filter, filter.Label(model.Text)))];
        SelectedChoice = Choices[(int)LogFilter.Info];
    }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>The filters for a picker.</summary>
    public IReadOnlyList<LogFilterChoice> Choices { get; }

    /// <summary>The filter that is on.</summary>
    [ObservableProperty]
    public partial LogFilterChoice SelectedChoice { get; set; }

    /// <summary>What the user is searching for; empty shows every line of the level.</summary>
    [ObservableProperty]
    public partial string Query { get; set; } = string.Empty;

    /// <summary>The lines that pass the filter and the search.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<LogEntry> Lines { get; private set; } = [];

    /// <summary>The view is scrolled to the newest line, so a new line scrolls into view by itself.</summary>
    [ObservableProperty]
    public partial bool IsAtEnd { get; set; } = true;

    /// <summary>There is nothing to show and no search narrowed it down: the log is empty.</summary>
    public bool ShowsEmptyLog => Lines.Count == 0 && Query.Length == 0 && !IsWaiting;

    /// <summary>A search found nothing.</summary>
    public bool ShowsNoMatch => Lines.Count == 0 && Query.Length > 0;

    /// <summary>The log has had the moment it needs to bring the lines that are already there.</summary>
    [ObservableProperty]
    public partial bool IsWaiting { get; private set; } = true;

    /// <summary>Counts the requests to move the focus to the search field.</summary>
    [ObservableProperty]
    public partial int SearchFocusRequest { get; private set; }

    /// <inheritdoc />
    public void Activate()
    {
        // Following already: a second stream would ask the daemon for the tail again for nothing.
        if (_following is not null)
        {
            return;
        }

        IsWaiting = true;
        _following = new CancellationTokenSource();
        _followingTask = FollowAsync(_following.Token);
        _model.PropertyChanged += OnModelChanged;
    }

    /// <inheritdoc />
    public void Deactivate()
    {
        _model.PropertyChanged -= OnModelChanged;
        _following?.Cancel();
        _following?.Dispose();
        _following = null;
    }

    /// <inheritdoc />
    public void Dispose() => Deactivate();

    /// <summary>Waits for the log to stop, for the tests and for a page that is closing.</summary>
    public Task StoppedAsync() => _followingTask;

    /// <summary>Puts the lines on screen on the clipboard, one per line.</summary>
    [RelayCommand]
    public void Copy() => _clipboard.SetText(string.Join('\n', Lines.Select(line => line.PlainText)));

    /// <summary>Empties the search.</summary>
    [RelayCommand]
    public void ClearQuery() => Query = string.Empty;

    /// <summary>Scrolls to the newest line.</summary>
    [RelayCommand]
    public void ScrollToLatest() => IsAtEnd = true;

    partial void OnSelectedChoiceChanged(LogFilterChoice value) => Refilter();

    partial void OnQueryChanged(string value) => Refilter();

    private async Task FollowAsync(CancellationToken cancellationToken)
    {
        // The log is empty for a moment while the buffered tail comes; saying so at once would flash.
        _ = SettleAsync(cancellationToken);
        await _tail.RunAsync(_model.Store, _profileId, cancellationToken);
    }

    private async Task SettleAsync(CancellationToken cancellationToken)
    {
        try
        {
            await Task.Delay(LogTail.TailGracePeriod, _model.Time, cancellationToken);
            IsWaiting = false;
            OnPropertyChanged(nameof(ShowsEmptyLog));
        }
        catch (OperationCanceledException)
        {
            // The page was left.
        }
    }

    private void OnModelChanged(object? sender, System.ComponentModel.PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(AppModel.SearchRequest))
        {
            SearchFocusRequest++;
        }
    }

    private void Refilter()
    {
        var filter = SelectedChoice.Filter;
        Lines = [.. _tail.Entries.Where(entry => filter.Includes(entry.Level) && (Query.Length == 0 || entry.Text.Contains(Query, StringComparison.CurrentCultureIgnoreCase)))];
        OnPropertyChanged(nameof(ShowsEmptyLog));
        OnPropertyChanged(nameof(ShowsNoMatch));
    }
}
