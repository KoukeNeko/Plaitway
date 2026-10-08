using System.ComponentModel;
using System.Globalization;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Dialogs;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Pages;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A page of Diagnostics and its words.</summary>
/// <param name="Page">The page.</param>
/// <param name="Label">Its words.</param>
public sealed record DiagnosticsTab(DiagnosticsPage Page, string Label);

/// <summary>A route the daemon owns.</summary>
/// <param name="Prefix">The network prefix.</param>
/// <param name="Detail">"Office · Tunnel · Plaitway-x": whose it is, what it is for, where it goes.</param>
/// <param name="StateLabel">Whether it is in effect.</param>
/// <param name="Icon">The shape of the state.</param>
/// <param name="Tone">The colour family of the state.</param>
public sealed record OwnedRouteRow(string Prefix, string Detail, string StateLabel, StatusIcon Icon, StatusTone Tone);

/// <summary>A route that is in the routing table although nothing owns it.</summary>
/// <param name="Key">What to pass to the daemon to remove it.</param>
/// <param name="Prefix">The network prefix.</param>
/// <param name="Via">The gateway and the interface.</param>
/// <param name="Reason">Why the daemon thinks it is stale.</param>
/// <param name="InstalledByPlaitway">The journal says Plaitway installed it.</param>
/// <param name="InstalledByLabel">What says so, in words.</param>
/// <param name="RemoveLabel">The words of the button that removes it.</param>
/// <param name="RemoveName">What a screen reader calls that button: the action and the route, because there is one such button per route.</param>
public sealed record StaleRouteRow(
    string Key, string Prefix, string Via, string Reason, bool InstalledByPlaitway, string InstalledByLabel, string RemoveLabel, string RemoveName);

/// <summary>A change the daemon made to routes or DNS.</summary>
/// <param name="Title">"route 10.0.0.0/8": what kind of entry and which.</param>
/// <param name="Caption">When, and for which profile.</param>
/// <param name="State">What became of it: pending, applied, removed.</param>
public sealed record JournalRow(string Title, string Caption, string State);

/// <summary>
/// What the helper knows about the network, the routes it owns, the resolver entries it set and what it changed lately,
/// and the stale routes it found; and the helper's own log. The reading is polled only while it is on screen: on the log
/// page every call would show up in the log being read.
/// </summary>
public sealed partial class DiagnosticsViewModel : ObservableObject, IPageLifecycle, IDisposable
{
    private const int JournalRowsShown = 50;

    private readonly AppModel _model;
    private readonly IClipboard _clipboard;
    private readonly IDialogService _dialogs;
    private readonly DiagnosticsModel _reading;
    private CancellationTokenSource? _polling;
    private bool _isActive;

    /// <summary>Makes the page.</summary>
    public DiagnosticsViewModel(AppModel model, IClipboard clipboard, IDialogService dialogs)
    {
        _model = model;
        _clipboard = clipboard;
        _dialogs = dialogs;
        _reading = new DiagnosticsModel(model.Store, model.Errors, model.Time);
        Tabs = [.. SectionLabels.DiagnosticsPages.Select(page => new DiagnosticsTab(page, page.Label(model.Text)))];
        HelperLog = new LogsViewModel(model, string.Empty, clipboard);
        _reading.PropertyChanged += OnReadingChanged;
        _model.PropertyChanged += OnModelChanged;
        Show(_reading.Reading);
    }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>The two pages.</summary>
    public IReadOnlyList<DiagnosticsTab> Tabs { get; }

    /// <summary>The page that is open.</summary>
    public DiagnosticsTab SelectedTab
    {
        get => Tabs[(int)_model.DiagnosticsPage];
        set
        {
            if (value is not null)
            {
                _model.DiagnosticsPage = value.Page;
            }
        }
    }

    /// <summary>The label of the overview page.</summary>
    public string OverviewLabel => Tabs[(int)DiagnosticsPage.Overview].Label;

    /// <summary>The label of the page of the helper's log.</summary>
    public string HelperLogLabel => Tabs[(int)DiagnosticsPage.DaemonLog].Label;

    /// <summary>The position of the open page, for a tab control that counts.</summary>
    public int SelectedTabIndex
    {
        get => (int)_model.DiagnosticsPage;
        set
        {
            if (value >= 0 && value < Tabs.Count)
            {
                _model.DiagnosticsPage = Tabs[value].Page;
            }
        }
    }

    /// <summary>The overview is the page that is open.</summary>
    public bool ShowsOverview => _model.DiagnosticsPage == DiagnosticsPage.Overview;

    /// <summary>The helper's log is the page that is open.</summary>
    public bool ShowsHelperLog => _model.DiagnosticsPage == DiagnosticsPage.DaemonLog;

    /// <summary>The helper's own log.</summary>
    public LogsViewModel HelperLog { get; }

    /// <summary>The daemon has been read at least once.</summary>
    [ObservableProperty]
    public partial bool HasReading { get; private set; }

    /// <summary>Why the last read failed; the reading before it stays on screen.</summary>
    [ObservableProperty]
    public partial string? Failure { get; private set; }

    /// <summary>The default gateway, the interfaces and the last change of the network; only what is known.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<DetailRow> Network { get; private set; } = [];

    /// <summary>The routes the daemon owns.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<OwnedRouteRow> OwnedRoutes { get; private set; } = [];

    /// <summary>Routes nothing owns; what needs doing comes first.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<StaleRouteRow> StaleRoutes { get; private set; } = [];

    /// <summary>The resolver entries the daemon set.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<string> ResolverEntries { get; private set; } = [];

    /// <summary>What the daemon changed lately, newest first.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<JournalRow> Journal { get; private set; } = [];

    /// <summary>There are stale routes.</summary>
    public bool HasStaleRoutes => StaleRoutes.Count > 0;

    /// <summary>The daemon owns no routes.</summary>
    public bool HasNoOwnedRoutes => OwnedRoutes.Count == 0;

    /// <summary>A resync is under way.</summary>
    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(ResyncCommand))]
    public partial bool IsResyncing { get; private set; }

    /// <inheritdoc />
    public void Activate()
    {
        _isActive = true;
        UpdateLiveReadings();
    }

    /// <inheritdoc />
    public void Deactivate()
    {
        _isActive = false;
        UpdateLiveReadings();
    }

    /// <inheritdoc />
    public void Dispose()
    {
        Deactivate();
        _reading.PropertyChanged -= OnReadingChanged;
        _model.PropertyChanged -= OnModelChanged;
        HelperLog.Dispose();
    }

    /// <summary>Reads the daemon now.</summary>
    public Task RefreshAsync() => _reading.RefreshAsync();

    /// <summary>Puts the report on the clipboard: plain text for a bug report, without log lines.</summary>
    [RelayCommand(CanExecute = nameof(HasReading))]
    public void CopyReport()
    {
        if (_reading.Reading is { } reading)
        {
            _clipboard.SetText(DiagnosticsReport.Text(reading, _model.AppVersion, _model.Store.DaemonInfo, Text, _model.ProfileName));
        }
    }

    /// <summary>Asks the daemon to read the network again and rebuild what it owns.</summary>
    [RelayCommand(CanExecute = nameof(CanResync))]
    public async Task ResyncAsync()
    {
        IsResyncing = true;
        try
        {
            await _model.ResyncAsync();
            await _reading.RefreshAsync();
        }
        finally
        {
            IsResyncing = false;
        }
    }

    /// <summary>Asks whether to remove a stale route, and removes it when the user says so.</summary>
    [RelayCommand]
    public async Task RemoveStaleRouteAsync(StaleRouteRow route)
    {
        var request = new ChoiceRequest(
            Text.RemoveRouteQuestion(route.Prefix),
            null,
            [new Choice(Text.Remove, ChoiceRole.Destructive), new Choice(Text.Cancel, ChoiceRole.Cancel)]);
        if (await _dialogs.AskAsync(request) == 0)
        {
            await _model.RemoveStaleRouteAsync(route.Key);
            await _reading.RefreshAsync();
        }
    }

    private bool CanResync() => !IsResyncing;

    partial void OnHasReadingChanged(bool value) => CopyReportCommand.NotifyCanExecuteChanged();

    private void OnReadingChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(DiagnosticsModel.Reading))
        {
            Show(_reading.Reading);
        }
        else if (args.PropertyName == nameof(DiagnosticsModel.Failure))
        {
            Failure = _reading.Failure;
        }
    }

    private void OnModelChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName != nameof(AppModel.DiagnosticsPage))
        {
            return;
        }

        OnPropertyChanged(nameof(SelectedTab));
        OnPropertyChanged(nameof(SelectedTabIndex));
        OnPropertyChanged(nameof(ShowsOverview));
        OnPropertyChanged(nameof(ShowsHelperLog));
        UpdateLiveReadings();
    }

    /// <summary>Each page reads from the daemon only while it is on screen: the overview polls, the log follows.</summary>
    private void UpdateLiveReadings()
    {
        UpdatePolling();
        UpdateHelperLog();
    }

    private void UpdatePolling()
    {
        var shouldPoll = _isActive && ShowsOverview;
        if (shouldPoll && _polling is null)
        {
            _polling = new CancellationTokenSource();
            _ = _reading.RunAsync(_polling.Token);
        }
        else if (!shouldPoll && _polling is not null)
        {
            _polling.Cancel();
            _polling.Dispose();
            _polling = null;
        }
    }

    private void UpdateHelperLog()
    {
        if (_isActive && ShowsHelperLog)
        {
            HelperLog.Activate();
        }
        else
        {
            HelperLog.Deactivate();
        }
    }

    private void Show(Diagnostics? reading)
    {
        HasReading = reading is not null;
        if (reading is null)
        {
            return;
        }

        Network = NetworkRows(reading.Network ?? new NetworkInfo());
        OwnedRoutes = [.. reading.OwnedRoutes.Select(OwnedRouteOf)];
        StaleRoutes = [.. reading.StaleRoutes.Select(StaleRouteOf)];
        ResolverEntries = [.. reading.ResolverEntries];
        Journal = [.. reading.RecentJournal.Reverse().Take(JournalRowsShown).Select(JournalRowOf)];
        OnPropertyChanged(nameof(HasStaleRoutes));
        OnPropertyChanged(nameof(HasNoOwnedRoutes));
    }

    private List<DetailRow> NetworkRows(NetworkInfo network)
    {
        List<DetailRow> rows = [];
        if (network.DefaultGatewayV4.Length > 0)
        {
            rows.Add(new DetailRow(Text.DefaultGateway, $"{network.DefaultGatewayV4} · {network.DefaultInterfaceV4}"));
        }

        if (network.DefaultGatewayV6.Length > 0)
        {
            rows.Add(new DetailRow(Text.DefaultGatewayIPv6, $"{network.DefaultGatewayV6} · {network.DefaultInterfaceV6}"));
        }

        if (network.Interfaces.Count > 0)
        {
            rows.Add(new DetailRow(Text.Interfaces, string.Join(", ", network.Interfaces)));
        }

        if (network.LastChange is { } change)
        {
            rows.Add(new DetailRow(Text.LastChange, $"{change.ToDateTimeOffset().ToLocalTime().ToString("T", Text.Culture)} · {network.LastChangeReason}"));
        }

        return rows;
    }

    private OwnedRouteRow OwnedRouteOf(OwnedRoute route) => new(
        route.Prefix,
        $"{_model.ProfileName(route.Owner) ?? route.Owner} · {route.Kind.Label(Text)} · {route.Via}",
        route.State.Label(Text),
        route.State.Icon(),
        route.State.Tone());

    private StaleRouteRow StaleRouteOf(StaleRoute route) => new(
        route.Key,
        route.Prefix,
        string.Join(" · ", new[] { route.Gateway, route.Interface }.Where(part => part.Length > 0)),
        route.Reason,
        route.Owned,
        Text.InstalledByPlaitway,
        Text.Remove,
        $"{Text.Remove} {route.Prefix}");

    private JournalRow JournalRowOf(JournalEntry entry)
    {
        var time = entry.Time is { } stamp ? stamp.ToDateTimeOffset().ToLocalTime().ToString("T", Text.Culture) : string.Empty;
        var owner = entry.Owner.Length == 0 ? string.Empty : _model.ProfileName(entry.Owner) ?? entry.Owner;
        return new JournalRow(
            string.Create(CultureInfo.InvariantCulture, $"{entry.Kind} {entry.Key}"),
            string.Join(" · ", new[] { time, owner }.Where(part => part.Length > 0)),
            entry.State);
    }
}
