using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>
/// What the profile has put in the routing table and in DNS, and for each entry whether it is in effect and, when it
/// is not, why.
/// </summary>
public sealed partial class RoutesViewModel : ProfileTabViewModel
{
    private readonly IClipboard _clipboard;

    /// <summary>Makes the routes tab of the profile <paramref name="profileId"/>.</summary>
    public RoutesViewModel(AppModel model, string profileId, IClipboard clipboard)
        : base(model, profileId)
    {
        _clipboard = clipboard;
        Follow();
    }

    /// <summary>The routes the daemon installed, or while the profile is not connected the prefixes it names itself.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<RouteRow> Routes { get; private set; } = [];

    /// <summary>The DNS entries the daemon installed.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<DnsRow> Dns { get; private set; } = [];

    /// <summary>The servers the profile names, shown while it is not connected.</summary>
    [ObservableProperty]
    public partial string DeclaredDns { get; private set; } = string.Empty;

    /// <summary>There is nothing to list at all.</summary>
    [ObservableProperty]
    public partial bool IsEmpty { get; private set; } = true;

    /// <summary>There is something to list in the DNS section.</summary>
    [ObservableProperty]
    public partial bool HasDns { get; private set; }

    /// <summary>Puts the prefixes of the rows with these ids, one per line, on the clipboard.</summary>
    [RelayCommand]
    public void CopyPrefixes(IReadOnlyCollection<int> rowIds)
    {
        if (rowIds.Count > 0)
        {
            _clipboard.SetText(string.Join('\n', Routes.Where(row => rowIds.Contains(row.Id)).Select(row => row.Prefix)));
        }
    }

    /// <inheritdoc />
    protected override void Refresh(Profile profile)
    {
        Routes = RouteRow.For(profile, Text, Model.ProfileName);
        Dns = DnsRow.For(profile, Text);
        DeclaredDns = string.Join(", ", profile.Summary?.DnsServers ?? []);
        HasDns = Dns.Count > 0 || DeclaredDns.Length > 0;
        IsEmpty = Routes.Count == 0 && !HasDns;
    }
}
