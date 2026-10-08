using System.Globalization;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A label and a value on one line.</summary>
/// <param name="Label">What the value is.</param>
/// <param name="Value">The value; may be several lines.</param>
public sealed record DetailRow(string Label, string Value);

/// <summary>A point of a chart, each coordinate between 0 and 1 (the origin is the bottom left).</summary>
/// <param name="X">From the oldest sample to the newest.</param>
/// <param name="Y">From nothing to the highest rate on the chart.</param>
public readonly record struct ChartPoint(double X, double Y);

/// <summary>
/// How much moves through the tunnel, as the two lines of a small chart on one scale: received as a filled area and sent
/// as a line, so that the two differ by shape and not by colour.
/// </summary>
/// <param name="Received">The received rate, a point per sample.</param>
/// <param name="Sent">The sent rate, a point per sample.</param>
public sealed record TrafficChart(IReadOnlyList<ChartPoint> Received, IReadOnlyList<ChartPoint> Sent)
{
    /// <summary>A chart never scales to less than this, so that a trickle does not fill it.</summary>
    private const double FloorBytesPerSecond = 1024;

    /// <summary>No samples yet.</summary>
    public static TrafficChart Empty { get; } = new([], []);

    /// <summary>The chart of <paramref name="samples"/>, oldest first.</summary>
    public static TrafficChart Of(IReadOnlyList<TrafficSample> samples)
    {
        if (samples.Count == 0)
        {
            return Empty;
        }

        var peak = Math.Max(samples.Max(sample => Math.Max(sample.Received, sample.Sent)), FloorBytesPerSecond);
        double X(int index) => samples.Count == 1 ? 1 : (double)index / (samples.Count - 1);
        return new TrafficChart(
            [.. samples.Select((sample, index) => new ChartPoint(X(index), sample.Received / peak))],
            [.. samples.Select((sample, index) => new ChartPoint(X(index), sample.Sent / peak))]);
    }
}

/// <summary>The state, why it is so, and for how long: what a person opens a profile to see.</summary>
public sealed partial class OverviewViewModel : ProfileTabViewModel
{
    private const string NoRate = "–";

    private readonly IClipboard _clipboard;
    private DateTimeOffset? _connectedSince;

    /// <summary>Makes the overview of the profile <paramref name="profileId"/>.</summary>
    public OverviewViewModel(AppModel model, string profileId, IClipboard clipboard)
        : base(model, profileId)
    {
        _clipboard = clipboard;
        Follow();
    }

    /// <summary>The state in a word.</summary>
    [ObservableProperty]
    public partial string StateLabel { get; private set; } = string.Empty;

    /// <summary>The shape of the state.</summary>
    [ObservableProperty]
    public partial StatusIcon Icon { get; private set; }

    /// <summary>The colour family of the state.</summary>
    [ObservableProperty]
    public partial StatusTone Tone { get; private set; }

    /// <summary>The profile is failing, so its cause is shown as an error.</summary>
    [ObservableProperty]
    public partial bool IsFailed { get; private set; }

    /// <summary>What the daemon says went wrong; empty when nothing did.</summary>
    [ObservableProperty]
    public partial string Cause { get; private set; } = string.Empty;

    /// <summary>There is a cause to show.</summary>
    public bool HasCause => Cause.Length > 0;

    /// <summary>The profile is connected and knows since when.</summary>
    [ObservableProperty]
    public partial bool ShowsUptime { get; private set; }

    /// <summary>How long the profile has been connected, as hours:minutes:seconds; see <see cref="RefreshUptime"/>.</summary>
    [ObservableProperty]
    public partial string UptimeText { get; private set; } = string.Empty;

    /// <summary>When the connection began, in the user's own format.</summary>
    [ObservableProperty]
    public partial string ConnectedSinceText { get; private set; } = string.Empty;

    /// <summary>How many routes the profile asks for and the system does not have.</summary>
    [ObservableProperty]
    public partial int LostRoutes { get; private set; }

    /// <summary>What the daemon warns about.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<string> Warnings { get; private set; } = [];

    /// <summary>Something needs the user's attention: routes not in effect, or a warning.</summary>
    public bool NeedsAttention => LostRoutes > 0 || Warnings.Count > 0;

    /// <summary>There are routes not in effect.</summary>
    public bool HasLostRoutes => LostRoutes > 0;

    /// <summary>The profile is connected, so there is traffic to show.</summary>
    [ObservableProperty]
    public partial bool ShowsTraffic { get; private set; }

    /// <summary>How fast data arrives, or a dash while there is no rate yet.</summary>
    [ObservableProperty]
    public partial string ReceivedRate { get; private set; } = NoRate;

    /// <summary>How much has arrived since the connection began.</summary>
    [ObservableProperty]
    public partial string ReceivedTotal { get; private set; } = string.Empty;

    /// <summary>How fast data leaves, or a dash while there is no rate yet.</summary>
    [ObservableProperty]
    public partial string SentRate { get; private set; } = NoRate;

    /// <summary>How much has left since the connection began.</summary>
    [ObservableProperty]
    public partial string SentTotal { get; private set; } = string.Empty;

    /// <summary>The last two minutes of both rates.</summary>
    [ObservableProperty]
    public partial TrafficChart Chart { get; private set; } = TrafficChart.Empty;

    /// <summary>The remote end, the interface, the addresses and the DNS servers; only what the profile has.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<DetailRow> Details { get; private set; } = [];

    /// <summary>The WireGuard public key, to give to the peer; empty for any other profile.</summary>
    [ObservableProperty]
    public partial string PublicKey { get; private set; } = string.Empty;

    /// <summary>There is a public key to show.</summary>
    public bool HasPublicKey => PublicKey.Length > 0;

    /// <summary>There is something to list under the state: the details or a public key.</summary>
    public bool HasDetails => Details.Count > 0 || HasPublicKey;

    partial void OnDetailsChanged(IReadOnlyList<DetailRow> value) => OnPropertyChanged(nameof(HasDetails));

    /// <summary>Opens the routes tab.</summary>
    [RelayCommand]
    public void ShowRoutes() => Model.ProfileSection = ProfileSection.Routes;

    /// <summary>Puts the public key on the clipboard.</summary>
    [RelayCommand]
    public void CopyPublicKey() => _clipboard.SetText(PublicKey);

    /// <summary>Works out the uptime now. The view calls it once a second while the tab is on screen.</summary>
    public void RefreshUptime()
    {
        UptimeText = _connectedSince is { } since ? FormatUptime(Model.Time.GetUtcNow() - since) : string.Empty;
    }

    /// <inheritdoc />
    protected override void Refresh(Profile profile)
    {
        StateLabel = profile.State.Label(Text);
        Icon = profile.State.Icon();
        Tone = profile.State.Tone();
        IsFailed = profile.State == ProfileState.Failed;
        Cause = profile.LastError;
        OnPropertyChanged(nameof(HasCause));

        RefreshConnection(profile);
        RefreshAttention(profile);
        RefreshTraffic(profile);
        RefreshDetails(profile);
    }

    private static string FormatUptime(TimeSpan elapsed)
    {
        var seconds = Math.Max((long)elapsed.TotalSeconds, 0);
        return string.Create(CultureInfo.InvariantCulture, $"{seconds / 3600}:{seconds / 60 % 60:00}:{seconds % 60:00}");
    }

    private void RefreshConnection(Profile profile)
    {
        _connectedSince = profile.State == ProfileState.Connected && profile.Status?.ConnectedSince is { } since ? since.ToDateTimeOffset() : null;
        ShowsUptime = _connectedSince is not null;
        ConnectedSinceText = _connectedSince is { } time ? time.ToLocalTime().ToString("g", Text.Culture) : string.Empty;
        RefreshUptime();
    }

    private void RefreshAttention(Profile profile)
    {
        LostRoutes = profile.Status?.Routes.Count(route => route.State.IsLost()) ?? 0;
        Warnings = [.. profile.Status?.Warnings ?? []];
        OnPropertyChanged(nameof(NeedsAttention));
        OnPropertyChanged(nameof(HasLostRoutes));
    }

    private void RefreshTraffic(Profile profile)
    {
        ShowsTraffic = profile.State == ProfileState.Connected;
        var rate = Model.Traffic.RateOf(ProfileId);
        ReceivedRate = rate is { } received ? Formatting.Rate(received.Received, Text) : NoRate;
        SentRate = rate is { } sent ? Formatting.Rate(sent.Sent, Text) : NoRate;
        ReceivedTotal = Formatting.Bytes(profile.Status?.RxBytes ?? 0, Text.Culture);
        SentTotal = Formatting.Bytes(profile.Status?.TxBytes ?? 0, Text.Culture);
        Chart = TrafficChart.Of(Model.Traffic.SamplesOf(ProfileId));
    }

    private void RefreshDetails(Profile profile)
    {
        var status = profile.Status;
        var summary = profile.Summary;
        var remote = status?.Remote is { Length: > 0 } inUse
            ? inUse
            : string.Join('\n', (summary?.Endpoints ?? []).Select(Formatting.Endpoint));
        var addresses = string.Join(", ", status?.Addresses.Count > 0 ? status.Addresses : summary?.Addresses ?? []);
        var dns = string.Join(", ", summary?.DnsServers ?? []);

        PublicKey = summary?.PublicKey ?? string.Empty;
        OnPropertyChanged(nameof(HasPublicKey));
        Details = [.. new (string Label, string Value)[]
        {
            (Text.Remote, remote),
            (Text.Interface, status?.InterfaceName ?? string.Empty),
            (Text.Addresses, addresses),
            (Text.DNS, dns),
        }.Where(row => row.Value.Length > 0).Select(row => new DetailRow(row.Label, row.Value))];
    }
}
