using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A tab of a profile and its words.</summary>
/// <param name="Section">The page.</param>
/// <param name="Label">Its words.</param>
public sealed record SectionTab(ProfileSection Section, string Label);

/// <summary>
/// A profile's page: its name and state, its five tabs, and the buttons that act on the profile as a whole, which stay at
/// the bottom of every tab. The tabs are made when first opened and live as long as the page does.
/// </summary>
public sealed partial class ProfilePageViewModel : ObservableObject, IDisposable
{
    private readonly AppModel _model;
    private ProfileSection? _shown;

    /// <summary>Makes the page of the profile <paramref name="profileId"/>.</summary>
    public ProfilePageViewModel(AppModel model, string profileId, IClipboard clipboard)
    {
        _model = model;
        ProfileId = profileId;
        Overview = new OverviewViewModel(model, profileId, clipboard);
        Routes = new RoutesViewModel(model, profileId, clipboard);
        Logs = new LogsViewModel(model, profileId, clipboard);
        Configuration = new ConfigurationViewModel(model, profileId);
        Settings = new ProfileSettingsViewModel(model, profileId);
        Tabs = [.. SectionLabels.ProfileSections.Select(section => new SectionTab(section, section.Label(model.Text)))];
        _model.Store.PropertyChanged += OnStoreChanged;
        _model.PropertyChanged += OnModelChanged;
        Refresh();
        ShowSection(_model.ProfileSection);
    }

    /// <summary>The profile.</summary>
    public string ProfileId { get; }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>The five tabs, in order.</summary>
    public IReadOnlyList<SectionTab> Tabs { get; }

    /// <summary>The label of the overview tab; the five labels are for a tab control whose items are written out.</summary>
    public string OverviewLabel => Tabs[(int)ProfileSection.Overview].Label;

    /// <summary>The label of the routes and DNS tab.</summary>
    public string RoutesLabel => Tabs[(int)ProfileSection.Routes].Label;

    /// <summary>The label of the log tab.</summary>
    public string LogsLabel => Tabs[(int)ProfileSection.Logs].Label;

    /// <summary>The label of the configuration tab.</summary>
    public string ConfigurationLabel => Tabs[(int)ProfileSection.Configuration].Label;

    /// <summary>The label of the settings tab.</summary>
    public string SettingsLabel => Tabs[(int)ProfileSection.Settings].Label;

    /// <summary>The overview tab.</summary>
    public OverviewViewModel Overview { get; }

    /// <summary>The routes and DNS tab.</summary>
    public RoutesViewModel Routes { get; }

    /// <summary>The log tab.</summary>
    public LogsViewModel Logs { get; }

    /// <summary>The configuration tab.</summary>
    public ConfigurationViewModel Configuration { get; }

    /// <summary>The settings tab.</summary>
    public ProfileSettingsViewModel Settings { get; }

    /// <summary>The tab that is open; the user's choice is kept when another profile is selected.</summary>
    public SectionTab SelectedTab
    {
        get => Tabs[(int)_model.ProfileSection];
        set
        {
            if (value is not null)
            {
                _model.ProfileSection = value.Section;
            }
        }
    }

    /// <summary>The position of the open tab, for a tab control that counts.</summary>
    public int SelectedTabIndex
    {
        get => (int)_model.ProfileSection;
        set
        {
            if (value >= 0 && value < Tabs.Count)
            {
                _model.ProfileSection = Tabs[value].Section;
            }
        }
    }

    /// <summary>The name of the profile.</summary>
    [ObservableProperty]
    public partial string Title { get; private set; } = string.Empty;

    /// <summary>"OpenVPN · Connected".</summary>
    [ObservableProperty]
    public partial string Subtitle { get; private set; } = string.Empty;

    /// <summary>The shape of the state.</summary>
    [ObservableProperty]
    public partial StatusIcon Icon { get; private set; }

    /// <summary>The colour family of the state.</summary>
    [ObservableProperty]
    public partial StatusTone Tone { get; private set; }

    /// <summary>The view model of the open tab.</summary>
    public object CurrentTab => TabOf(_model.ProfileSection);

    /// <summary>The profile failed, so the bar offers to try again.</summary>
    [ObservableProperty]
    public partial bool ShowsRetry { get; private set; }

    /// <summary>"Connect", or "Disconnect" while the profile is on.</summary>
    [ObservableProperty]
    public partial string ToggleLabel { get; private set; } = string.Empty;

    /// <summary>Connecting is the main thing to do, so its button is the prominent one; disconnecting is not.</summary>
    [ObservableProperty]
    public partial bool ToggleIsProminent { get; private set; } = true;

    /// <summary>The profile is not disconnecting: there is something to ask of it.</summary>
    [ObservableProperty]
    public partial bool CanToggle { get; private set; } = true;

    /// <inheritdoc />
    public void Dispose()
    {
        _model.Store.PropertyChanged -= OnStoreChanged;
        _model.PropertyChanged -= OnModelChanged;
        if (_shown is { } shown)
        {
            (TabOf(shown) as IPageLifecycle)?.Deactivate();
        }

        Overview.Dispose();
        Routes.Dispose();
        Logs.Dispose();
        Configuration.Dispose();
        Settings.Dispose();
    }

    /// <summary>Connects the profile, or disconnects it when it is on.</summary>
    [RelayCommand]
    public async Task ToggleAsync()
    {
        if (_model.Store.Find(ProfileId) is { } profile)
        {
            await _model.SetEnabledAsync(ProfileId, !profile.DesiredEnabled);
        }
    }

    /// <summary>Tries a failed profile again.</summary>
    [RelayCommand]
    public Task RetryAsync() => _model.SetEnabledAsync(ProfileId, enabled: true);

    private void OnStoreChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(ProfileStore.Profiles))
        {
            Refresh();
        }
    }

    private void OnModelChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(AppModel.ProfileSection))
        {
            OnPropertyChanged(nameof(SelectedTab));
            OnPropertyChanged(nameof(SelectedTabIndex));
            OnPropertyChanged(nameof(CurrentTab));
            ShowSection(_model.ProfileSection);
        }
    }

    private void Refresh()
    {
        if (_model.Store.Find(ProfileId) is not { } profile)
        {
            return;
        }

        Title = profile.Name;
        Subtitle = $"{profile.Kind.Label(Text)} · {profile.State.Label(Text)}";
        Icon = profile.State.Icon();
        Tone = profile.State.Tone();
        ShowsRetry = profile.State == ProfileState.Failed;
        ToggleLabel = profile.DesiredEnabled ? Text.Disconnect : Text.Connect;
        ToggleIsProminent = !profile.DesiredEnabled;
        CanToggle = profile.State != ProfileState.Disconnecting;
    }

    private void ShowSection(ProfileSection section)
    {
        if (_shown == section)
        {
            return;
        }

        if (_shown is { } previous)
        {
            (TabOf(previous) as IPageLifecycle)?.Deactivate();
        }

        _shown = section;
        (TabOf(section) as IPageLifecycle)?.Activate();
    }

    private object TabOf(ProfileSection section) => section switch
    {
        ProfileSection.Overview => Overview,
        ProfileSection.Routes => Routes,
        ProfileSection.Logs => Logs,
        ProfileSection.Configuration => Configuration,
        _ => Settings,
    };
}
