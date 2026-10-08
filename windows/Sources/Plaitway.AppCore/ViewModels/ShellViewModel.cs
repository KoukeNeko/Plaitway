using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Text;
using Plaitway.AppCore.Tray;
using Plaitway.AppCore.Dialogs;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>A note above a page: something the user may want to act on.</summary>
/// <param name="Title">What it says.</param>
/// <param name="Detail">More, such as the two versions; null when there is nothing more.</param>
/// <param name="Hint">What to do when the app cannot do it; null otherwise.</param>
/// <param name="ActionLabel">The button's words; null when there is no button.</param>
/// <param name="Action">What the button does.</param>
public sealed record NoticeBanner(string Title, string? Detail, string? Hint, string? ActionLabel, SetupAction? Action);

/// <summary>The page of a window with no profile to show: it offers to import one.</summary>
public sealed class EmptyProfilesViewModel(ShellViewModel shell)
{
    /// <summary>The strings.</summary>
    public UiText Text => shell.Text;

    /// <summary>Asks for profile files.</summary>
    public IAsyncRelayCommand ImportCommand => shell.ImportCommand;
}

/// <summary>
/// The window: the sidebar, the page the sidebar selects, the setup page or a note above it when the helper is not
/// ready, and the dialogs. It routes by what is selected and holds no state of its own beyond the page that is open.
/// </summary>
public sealed partial class ShellViewModel : ObservableObject, IDisposable
{
    private readonly IClipboard _clipboard;
    private readonly IFilePicker _filePicker;
    private readonly IDialogService _dialogs;
    private string? _pageKey;
    private bool _hasOfferedInstallation;
    private bool _isWindowVisible;

    /// <summary>Makes the window's model.</summary>
    /// <param name="model">The app.</param>
    /// <param name="clipboard">The clipboard.</param>
    /// <param name="filePicker">The file dialog.</param>
    /// <param name="dialogs">The dialogs.</param>
    /// <param name="isWindowVisible">False for a start in the notification area: no page is made until the window is shown.</param>
    public ShellViewModel(AppModel model, IClipboard clipboard, IFilePicker filePicker, IDialogService dialogs, bool isWindowVisible = true)
    {
        Model = model;
        _isWindowVisible = isWindowVisible;
        _clipboard = clipboard;
        _filePicker = filePicker;
        _dialogs = dialogs;
        Sidebar = new SidebarViewModel(model);
        Setup = new SetupViewModel(model);
        Tray = new TrayViewModel(model);
        Tray.Requested += OnTrayRequested;
        Model.PropertyChanged += OnModelChanged;
        Model.Store.PropertyChanged += OnStoreChanged;
        ShowPage();
        RefreshBanner();
    }

    /// <summary>The window should come forward: the tray icon asked, a profile asks for credentials, or the helper needs installing.</summary>
    public event Action? ShowWindowRequested;

    /// <summary>The window should ask for profile files.</summary>
    public event Action? ImportRequested;

    /// <summary>Quit was chosen; the app has to ask what the profiles should do first.</summary>
    public event Action? QuitRequested;

    /// <summary>The app.</summary>
    public AppModel Model { get; }

    /// <summary>The strings.</summary>
    public UiText Text => Model.Text;

    /// <summary>The profiles, Diagnostics and Settings.</summary>
    public SidebarViewModel Sidebar { get; }

    /// <summary>What replaces the profiles while the helper is not ready.</summary>
    public SetupViewModel Setup { get; }

    /// <summary>The notification-area icon.</summary>
    public TrayViewModel Tray { get; }

    /// <summary>The page the sidebar selects: a <see cref="ProfilePageViewModel"/>, <see cref="DiagnosticsViewModel"/>, <see cref="AppSettingsViewModel"/> or <see cref="EmptyProfilesViewModel"/>.</summary>
    [ObservableProperty]
    public partial object? Page { get; private set; }

    /// <summary>The note above the page; null when there is none.</summary>
    [ObservableProperty]
    public partial NoticeBanner? Banner { get; private set; }

    /// <summary>There is a note above the page.</summary>
    public bool HasBanner => Banner is not null;

    /// <summary>The setup page replaces the profiles.</summary>
    public bool ShowsSetup => Setup.IsShown;

    /// <summary>The profiles and their pages are shown.</summary>
    public bool ShowsProfiles => !Setup.IsShown;

    /// <summary>Asks for profile files and imports them.</summary>
    [RelayCommand]
    public async Task ImportAsync()
    {
        if (!Model.Setup.IsUsable)
        {
            return;
        }

        var paths = await _filePicker.PickProfileFilesAsync();
        if (paths.Count > 0)
        {
            await Model.ImportFilesAsync(paths);
        }
    }

    /// <summary>Files can be dropped on the window: the helper answers and can store them.</summary>
    public bool CanAcceptDrop => Model.Setup.IsUsable;

    /// <summary>Something that can be dropped is over the window.</summary>
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowsDropHint))]
    public partial bool IsDropTargeted { get; private set; }

    /// <summary>The window outlines itself to say that a drop imports: the accent border of the drop on macOS.</summary>
    public bool ShowsDropHint => IsDropTargeted && CanAcceptDrop;

    /// <summary>What a drag does while it is over the window: the view sets it on enter and leave.</summary>
    public void SetDropTargeted(bool isTargeted) => IsDropTargeted = isTargeted;

    /// <summary>Imports what was dropped on the window; files only, and the items that are not files are reported.</summary>
    public async Task DropAsync(IEnumerable<DroppedItem> items)
    {
        // The drag is over; no event says so after a drop.
        IsDropTargeted = false;
        if (CanAcceptDrop)
        {
            await Model.ImportDroppedAsync(DropBatch.Of(items, Text));
        }
    }

    /// <summary>Switches every profile off; one that stays on is reported.</summary>
    [RelayCommand]
    public async Task DisconnectAllAsync() => _ = await Model.DisconnectAllAsync();

    /// <summary>
    /// The window is shown or hidden. A page that is not on screen does not keep calling the daemon, which the diagnostics
    /// poll and the log streams would otherwise do for as long as the app runs.
    /// </summary>
    public void SetWindowVisible(bool isVisible)
    {
        _isWindowVisible = isVisible;
        if (isVisible)
        {
            ShowPage();
            return;
        }

        DisposePage();
        _pageKey = null;
    }

    /// <summary>Does what the banner's button says.</summary>
    [RelayCommand]
    public Task PerformBannerActionAsync() => Banner?.Action is { } action ? Model.PerformAsync(action) : Task.CompletedTask;

    // What the menu of the macOS app does, as shortcuts of the window.

    /// <summary>Connects the selected profile, or disconnects it when it is on.</summary>
    public Task ToggleSelectedProfileAsync() =>
        Model.SelectedProfile is { } profile && Model.Setup.IsUsable && profile.State != ProfileState.Disconnecting
            ? Model.SetEnabledAsync(profile.Id, !profile.DesiredEnabled)
            : Task.CompletedTask;

    /// <summary>Moves the selected profile up one place.</summary>
    public Task MoveSelectedProfileAsync(int delta) =>
        Model.SelectedProfile is { } profile && Model.Setup.IsUsable ? Model.MoveAsync(profile.Id, delta) : Task.CompletedTask;

    /// <summary>Asks to delete the selected profile.</summary>
    public void DeleteSelectedProfile()
    {
        if (Model.SelectedProfile is { } profile && Model.Setup.IsUsable)
        {
            Model.RequestDeletion(profile.Id);
        }
    }

    /// <summary>Opens the tab with the number <paramref name="index"/> (0 is Overview) of the selected profile.</summary>
    public void ShowSection(int index)
    {
        if (Model.Setup.IsUsable && Model.Store.Profiles.Count > 0 && index >= 0 && index < SectionLabels.ProfileSections.Count)
        {
            if (Model.SelectedProfile is null)
            {
                Model.Selection = SidebarItem.Profile(Model.Store.Profiles[0].Id);
            }

            Model.ProfileSection = SectionLabels.ProfileSections[index];
        }
    }

    /// <summary>Opens Diagnostics.</summary>
    public void ShowDiagnostics() => Model.Selection = SidebarItem.Diagnostics;

    /// <summary>Opens the app's settings.</summary>
    public void ShowSettings() => Model.Selection = SidebarItem.Settings;

    /// <summary>The search field of a log, on the pages that have one.</summary>
    public void FindInPage()
    {
        var onLog = Model.SelectedProfile is not null && Model.ProfileSection == ProfileSection.Logs
            || Model.Selection is SidebarItem.DiagnosticsItem && Model.DiagnosticsPage == DiagnosticsPage.DaemonLog;
        if (onLog)
        {
            Model.RequestSearch();
        }
    }

    /// <inheritdoc />
    public void Dispose()
    {
        Model.PropertyChanged -= OnModelChanged;
        Model.Store.PropertyChanged -= OnStoreChanged;
        Tray.Requested -= OnTrayRequested;
        DisposePage();
        Sidebar.Dispose();
        Setup.Dispose();
        Tray.Dispose();
    }

    private void OnTrayRequested(ShellRequest request)
    {
        switch (request)
        {
            case ShellRequest.ShowWindow:
                ShowWindowRequested?.Invoke();
                break;
            case ShellRequest.ImportProfile:
                ShowWindowRequested?.Invoke();
                ImportRequested?.Invoke();
                break;
            case ShellRequest.ShowSettings:
                ShowSettings();
                ShowWindowRequested?.Invoke();
                break;
            default:
                QuitRequested?.Invoke();
                break;
        }
    }

    private void OnStoreChanged(object? sender, PropertyChangedEventArgs args)
    {
        // A profile asks for credentials while no window is open: the dialog needs one.
        if (args.PropertyName == nameof(Daemon.ProfileStore.CredentialPrompts) && Model.Store.CredentialPrompts.Count > 0)
        {
            ShowWindowRequested?.Invoke();
        }
    }

    private void OnModelChanged(object? sender, PropertyChangedEventArgs args)
    {
        switch (args.PropertyName)
        {
            case nameof(AppModel.Selection):
                ShowPage();
                break;
            case nameof(AppModel.Setup):
                RefreshBanner();
                OfferInstallationOnce();
                OnPropertyChanged(nameof(CanAcceptDrop));
                OnPropertyChanged(nameof(ShowsDropHint));
                break;
            case nameof(AppModel.KeepsProfilesInView):
                RefreshBanner();
                break;
            default:
                break;
        }

        if (args.PropertyName is nameof(AppModel.Setup) or nameof(AppModel.KeepsProfilesInView))
        {
            OnPropertyChanged(nameof(ShowsSetup));
            OnPropertyChanged(nameof(ShowsProfiles));
        }
    }

    /// <summary>
    /// First run: the window offers the install. The helper may still be answering (installed a moment ago), so it waits
    /// for the connection.
    /// </summary>
    private void OfferInstallationOnce()
    {
        if (_hasOfferedInstallation || Model.Setup.Kind == SetupKind.Connecting)
        {
            return;
        }

        _hasOfferedInstallation = true;
        if (Model.Setup.OffersInstallation)
        {
            ShowWindowRequested?.Invoke();
        }
    }

    private void RefreshBanner()
    {
        Banner = Model.Setup.Kind switch
        {
            SetupKind.VersionMismatch => VersionBanner(),
            SetupKind.NotResponding when Model.KeepsProfilesInView => new NoticeBanner(Text.HelperUnavailable, null, null, Text.Retry, SetupAction.Retry),
            _ => null,
        };
        OnPropertyChanged(nameof(HasBanner));
    }

    private NoticeBanner VersionBanner()
    {
        var versions = $"{Model.Setup.DaemonVersion} → {Model.Setup.AppVersion}";
        return Model.IsHelperExternal
            ? new NoticeBanner(Text.HelperOutOfDate, versions, Text.StartTheHelperAgainFromThisVersion, null, null)
            : new NoticeBanner(Text.HelperOutOfDate, versions, null, Text.ReinstallHelper, SetupAction.ReinstallHelper);
    }

    private void ShowPage()
    {
        // A selection can change while the window is hidden (another client deletes the profile, the first snapshot
        // arrives): a page made then would follow a log for nobody.
        if (!_isWindowVisible)
        {
            return;
        }

        var key = Model.Selection switch
        {
            SidebarItem.ProfileItem item when Model.Store.Find(item.Id) is not null => "profile:" + item.Id,
            SidebarItem.DiagnosticsItem => "diagnostics",
            SidebarItem.SettingsItem => "settings",
            _ => "empty",
        };
        if (key == _pageKey)
        {
            return;
        }

        DisposePage();
        _pageKey = key;
        Page = key switch
        {
            "diagnostics" => new DiagnosticsViewModel(Model, _clipboard, _dialogs),
            "settings" => new AppSettingsViewModel(Model),
            "empty" => new EmptyProfilesViewModel(this),
            _ => new ProfilePageViewModel(Model, key["profile:".Length..], _clipboard),
        };
        (Page as IPageLifecycle)?.Activate();
    }

    private void DisposePage()
    {
        (Page as IPageLifecycle)?.Deactivate();
        (Page as IDisposable)?.Dispose();
        Page = null;
    }
}
