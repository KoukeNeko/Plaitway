using System.Collections.ObjectModel;
using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A profile in the sidebar: its shield in the colour of its state, its name, and under it what it is and how it stands.</summary>
/// <param name="id">The profile.</param>
/// <param name="text">The strings.</param>
/// <param name="owner">The sidebar the row is in, which does what the row's menu asks.</param>
public sealed partial class ProfileRowViewModel(string id, UiText text, SidebarViewModel owner) : ObservableObject
{
    /// <summary>The profile's id.</summary>
    public string Id { get; } = id;

    /// <summary>The words of the menu's entries.</summary>
    public UiText Text { get; } = text;

    /// <summary>Connects the profile, or disconnects it when it is on.</summary>
    public IAsyncRelayCommand ToggleCommand { get; } = new AsyncRelayCommand(() => owner.ToggleAsync(id));

    /// <summary>Moves the profile up one place.</summary>
    public IAsyncRelayCommand MoveUpCommand { get; } = new AsyncRelayCommand(() => owner.MoveUpAsync(id));

    /// <summary>Moves the profile down one place.</summary>
    public IAsyncRelayCommand MoveDownCommand { get; } = new AsyncRelayCommand(() => owner.MoveDownAsync(id));

    /// <summary>Asks to delete the profile.</summary>
    public IRelayCommand DeleteCommand { get; } = new RelayCommand(() => owner.RequestDeletion(id));

    /// <summary>The profile's name.</summary>
    [ObservableProperty]
    public partial string Name { get; private set; } = string.Empty;

    /// <summary>"OpenVPN · Connected".</summary>
    [ObservableProperty]
    public partial string Subtitle { get; private set; } = string.Empty;

    /// <summary>The state in a word.</summary>
    [ObservableProperty]
    public partial string StateLabel { get; private set; } = string.Empty;

    /// <summary>The shape of the state.</summary>
    [ObservableProperty]
    public partial StatusIcon Icon { get; private set; }

    /// <summary>The colour family of the state.</summary>
    [ObservableProperty]
    public partial StatusTone Tone { get; private set; }

    /// <summary>What a screen reader says for the row: the name, what it is, and how it stands.</summary>
    [ObservableProperty]
    public partial string AutomationName { get; private set; } = string.Empty;

    /// <summary>The profile is switched on (or on its way), so the context menu offers to disconnect it.</summary>
    [ObservableProperty]
    public partial bool IsOn { get; private set; }

    /// <summary>The label of the action the context menu offers: Connect, or Disconnect when the profile is on.</summary>
    [ObservableProperty]
    public partial string ToggleLabel { get; private set; } = string.Empty;

    /// <summary>The profile is disconnecting: there is nothing to ask of it.</summary>
    [ObservableProperty]
    public partial bool CanToggle { get; private set; } = true;

    /// <summary>
    /// A list item is named by the text of the item it shows, so the row is called what a screen reader should say for it.
    /// </summary>
    public override string ToString() => AutomationName;

    /// <summary>Takes what the daemon says about the profile.</summary>
    public void Update(Profile profile)
    {
        var kind = profile.Kind.Label(Text);
        var state = profile.State.Label(Text);
        Name = profile.Name;
        StateLabel = state;
        Subtitle = $"{kind} · {state}";
        Icon = profile.State.Icon();
        Tone = profile.State.Tone();
        AutomationName = $"{profile.Name}, {kind}, {state}";
        IsOn = profile.DesiredEnabled;
        ToggleLabel = profile.DesiredEnabled ? Text.Disconnect : Text.Connect;
        CanToggle = profile.State != ProfileState.Disconnecting;
    }
}

/// <summary>
/// The sidebar: the profiles in priority order, which the user can reorder, then Diagnostics and Settings. The rows are
/// edited in place when the daemon reports a change, so that the selection and a drag in progress survive it.
/// </summary>
public sealed partial class SidebarViewModel : ObservableObject, IDisposable
{
    private readonly AppModel _model;

    /// <summary>Makes the sidebar for <paramref name="model"/>.</summary>
    public SidebarViewModel(AppModel model)
    {
        _model = model;
        _model.Store.PropertyChanged += OnStoreChanged;
        _model.PropertyChanged += OnModelChanged;
        Sync();
    }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>The profiles in priority order, highest first.</summary>
    public ObservableCollection<ProfileRowViewModel> Rows { get; } = [];

    /// <summary>There is at least one profile.</summary>
    public bool HasProfiles => Rows.Count > 0;

    /// <summary>What is selected, which is what the page shows.</summary>
    public SidebarItem? Selection
    {
        get => _model.Selection;
        set => _model.Selection = value;
    }

    /// <summary>The selected profile's id; null when Diagnostics, Settings or nothing is.</summary>
    public string? SelectedProfileId => (_model.Selection as SidebarItem.ProfileItem)?.Id;

    /// <summary>Diagnostics is selected.</summary>
    public bool IsDiagnosticsSelected => _model.Selection is SidebarItem.DiagnosticsItem;

    /// <summary>Settings is selected.</summary>
    public bool IsSettingsSelected => _model.Selection is SidebarItem.SettingsItem;

    /// <inheritdoc />
    public void Dispose()
    {
        _model.Store.PropertyChanged -= OnStoreChanged;
        _model.PropertyChanged -= OnModelChanged;
    }

    /// <summary>Selects a profile.</summary>
    public void SelectProfile(string id) => _model.Selection = SidebarItem.Profile(id);

    /// <summary>Selects Diagnostics.</summary>
    [RelayCommand]
    public void SelectDiagnostics() => _model.Selection = SidebarItem.Diagnostics;

    /// <summary>Selects Settings.</summary>
    [RelayCommand]
    public void SelectSettings() => _model.Selection = SidebarItem.Settings;

    /// <summary>Sends the order the user dragged the rows into. The daemon's answer comes back through the watch.</summary>
    [RelayCommand]
    public async Task ApplyDraggedOrderAsync()
    {
        var ids = Rows.Select(row => row.Id).ToList();
        if (!ids.SequenceEqual(_model.Store.Profiles.Select(profile => profile.Id)))
        {
            await _model.ApplyOrderAsync(ids);

            // The daemon may have refused: show what it has.
            Sync();
        }
    }

    /// <summary>Moves a profile up one place.</summary>
    [RelayCommand]
    public Task MoveUpAsync(string profileId) => _model.MoveAsync(profileId, -1);

    /// <summary>Moves a profile down one place.</summary>
    [RelayCommand]
    public Task MoveDownAsync(string profileId) => _model.MoveAsync(profileId, 1);

    /// <summary>Connects a profile, or disconnects it when it is on.</summary>
    [RelayCommand]
    public Task ToggleAsync(string profileId) =>
        _model.Store.Find(profileId) is { } profile ? _model.SetEnabledAsync(profileId, !profile.DesiredEnabled) : Task.CompletedTask;

    /// <summary>Asks to delete a profile.</summary>
    [RelayCommand]
    public void RequestDeletion(string profileId) => _model.RequestDeletion(profileId);

    private void OnStoreChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(ProfileStore.Profiles))
        {
            Sync();
        }
    }

    private void OnModelChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(AppModel.Selection))
        {
            OnPropertyChanged(nameof(Selection));
            OnPropertyChanged(nameof(SelectedProfileId));
            OnPropertyChanged(nameof(IsDiagnosticsSelected));
            OnPropertyChanged(nameof(IsSettingsSelected));
        }
    }

    private void Sync()
    {
        ObservableListSync.Sync(
            Rows,
            _model.Store.Profiles,
            row => row.Id,
            profile => profile.Id,
            profile =>
            {
                var row = new ProfileRowViewModel(profile.Id, _model.Text, this);
                row.Update(profile);
                return row;
            },
            (row, profile) => row.Update(profile));
        OnPropertyChanged(nameof(HasProfiles));
    }
}
