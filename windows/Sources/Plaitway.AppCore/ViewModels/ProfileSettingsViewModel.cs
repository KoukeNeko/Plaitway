using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Presentation;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A tunnel mode and its words, for a picker.</summary>
/// <param name="Mode">The mode.</param>
/// <param name="Label">Its words.</param>
public sealed record TunnelModeChoice(TunnelMode Mode, string Label);

/// <summary>How one profile behaves: its name, when it connects, and what it may take over.</summary>
public sealed partial class ProfileSettingsViewModel : ProfileTabViewModel
{
    private static readonly CredentialKind[] SavedKinds = [CredentialKind.UserPassword, CredentialKind.KeyPassphrase];

    private bool _isRefreshing;

    /// <summary>Makes the settings of the profile <paramref name="profileId"/>.</summary>
    public ProfileSettingsViewModel(AppModel model, string profileId)
        : base(model, profileId)
    {
        // What the daemon reports is not a change the user made, and neither is the first value of a picker.
        _isRefreshing = true;
        TunnelModes = [.. Labels.TunnelModeChoices.Select(mode => new TunnelModeChoice(mode, mode.Label(Text)))];
        SelectedTunnelMode = TunnelModes[0];
        Follow();
        _isRefreshing = false;
        _ = RefreshSavedCredentialsAsync();
    }

    /// <summary>The tunnel modes, in the order the picker lists them.</summary>
    public IReadOnlyList<TunnelModeChoice> TunnelModes { get; }

    /// <summary>The name the field shows; the profile gets it when the field is committed.</summary>
    [ObservableProperty]
    public partial string Name { get; set; } = string.Empty;

    /// <summary>Connect when the daemon starts.</summary>
    [ObservableProperty]
    public partial bool AutoConnect { get; set; }

    /// <summary>All traffic, only the profile's routes, or as the profile says.</summary>
    [ObservableProperty]
    public partial TunnelModeChoice SelectedTunnelMode { get; set; }

    /// <summary>The profile is WireGuard, which can leave the private ranges out of the tunnel.</summary>
    [ObservableProperty]
    public partial bool OffersExcludePrivateIps { get; private set; }

    /// <summary>The private address ranges stay outside the tunnel.</summary>
    [ObservableProperty]
    public partial bool ExcludePrivateIps { get; set; }

    /// <summary>The profile is not connected, so a change applies at once; otherwise it applies at the next connection.</summary>
    [ObservableProperty]
    public partial bool IsIdle { get; private set; } = true;

    /// <summary>What a change of the tunnel mode or the address ranges does while the profile is on; empty otherwise.</summary>
    public string AppliesLaterHint => IsIdle ? string.Empty : Text.AppliesAtNextConnection;

    /// <summary>What the exclusion of private addresses means while the profile is idle, and when it applies otherwise.</summary>
    public string ExcludePrivateIpsHint => IsIdle ? Text.PrivateAddressRangesStayOutsideTheTunnel : Text.AppliesAtNextConnection;

    /// <summary>The profile's place in the priority order, 1 being the highest.</summary>
    [ObservableProperty]
    public partial int Position { get; private set; } = 1;

    /// <summary>The profile is not the first.</summary>
    public bool CanMoveUp => Position > 1;

    /// <summary>The profile is not the last.</summary>
    public bool CanMoveDown => Position < Model.Store.Profiles.Count;

    /// <summary>Connect on Ethernet.</summary>
    [ObservableProperty]
    public partial bool OnDemandEthernet { get; set; }

    /// <summary>Connect on Wi-Fi.</summary>
    [ObservableProperty]
    public partial bool OnDemandWifi { get; set; }

    /// <summary>The credential store holds an answer for the profile.</summary>
    [ObservableProperty]
    public partial bool HasSavedCredentials { get; private set; }

    /// <summary>Gives the profile the name in the field, or puts the profile's own name back when the daemon refuses it.</summary>
    [RelayCommand]
    public async Task CommitNameAsync()
    {
        if (!await Model.RenameAsync(ProfileId, Name) && Profile is { } profile)
        {
            // The daemon may refuse the name (another profile has it); the field goes back to the name the profile still has.
            Name = profile.Name;
        }
    }

    /// <summary>Moves the profile up one place.</summary>
    [RelayCommand]
    public Task MoveUpAsync() => Model.MoveAsync(ProfileId, -1);

    /// <summary>Moves the profile down one place.</summary>
    [RelayCommand]
    public Task MoveDownAsync() => Model.MoveAsync(ProfileId, 1);

    /// <summary>Forgets the answers saved for the profile.</summary>
    [RelayCommand]
    public async Task ForgetCredentialsAsync()
    {
        await Model.Store.ForgetSavedCredentialsAsync(ProfileId);
        HasSavedCredentials = false;
    }

    /// <summary>Asks to delete the profile.</summary>
    [RelayCommand]
    public void RequestDeletion() => Model.RequestDeletion(ProfileId);

    /// <inheritdoc />
    protected override void Refresh(Profile profile)
    {
        _isRefreshing = true;
        try
        {
            var settings = profile.Settings ?? new ProfileSettings();
            if (!IsNameBeingEdited)
            {
                Name = profile.Name;
            }

            AutoConnect = settings.AutoConnect;
            SelectedTunnelMode = TunnelModes.First(choice => choice.Mode == (settings.TunnelMode == TunnelMode.Unspecified ? TunnelMode.Auto : settings.TunnelMode));
            OffersExcludePrivateIps = profile.Kind == ProfileKind.Wireguard;
            ExcludePrivateIps = settings.ExcludePrivateIps;
            OnDemandEthernet = settings.OnDemand?.Ethernet ?? false;
            OnDemandWifi = settings.OnDemand?.Wifi ?? false;
            IsIdle = profile.State is ProfileState.Disconnected or ProfileState.Failed;
            Position = Model.Store.Profiles.ToList().FindIndex(candidate => candidate.Id == ProfileId) + 1;
            OnPropertyChanged(nameof(AppliesLaterHint));
            OnPropertyChanged(nameof(ExcludePrivateIpsHint));
            OnPropertyChanged(nameof(CanMoveUp));
            OnPropertyChanged(nameof(CanMoveDown));
        }
        finally
        {
            _isRefreshing = false;
        }
    }

    /// <summary>Set by the view while the name field has the focus, so that the daemon's reports do not overwrite what is being typed.</summary>
    public bool IsNameBeingEdited { get; set; }

    partial void OnAutoConnectChanged(bool value) => Apply(settings => settings.AutoConnect = value);

    partial void OnSelectedTunnelModeChanged(TunnelModeChoice value) => Apply(settings => settings.TunnelMode = value.Mode);

    partial void OnExcludePrivateIpsChanged(bool value) => Apply(settings => settings.ExcludePrivateIps = value);

    partial void OnOnDemandEthernetChanged(bool value) => Apply(settings => OnDemandOf(settings).Ethernet = value);

    partial void OnOnDemandWifiChanged(bool value) => Apply(settings => OnDemandOf(settings).Wifi = value);

    private static OnDemandRules OnDemandOf(ProfileSettings settings) => settings.OnDemand ??= new OnDemandRules();

    /// <summary>A change the user made; what the daemon reports back is not one.</summary>
    private void Apply(Action<ProfileSettings> change)
    {
        if (!_isRefreshing)
        {
            _ = Model.ChangeSettingsAsync(ProfileId, change);
        }
    }

    private async Task RefreshSavedCredentialsAsync()
    {
        var found = false;
        foreach (var kind in SavedKinds)
        {
            found |= await Model.Store.HasSavedCredentialsAsync(ProfileId, kind);
        }

        HasSavedCredentials = found;
    }
}
