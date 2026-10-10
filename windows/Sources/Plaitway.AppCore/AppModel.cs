using CommunityToolkit.Mvvm.ComponentModel;
using Microsoft.Extensions.Logging;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Editing;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore;

/// <summary>What the app knows about itself that the model cannot find out.</summary>
/// <param name="AppVersion">The version of this app; null when unknown (a development build), so that the helper is not called out of date.</param>
/// <param name="IsOverridden">The daemon is the developer's (<c>PLAITWAY_SOCKET</c>): the app does not manage the helper.</param>
public sealed record AppEnvironment(string? AppVersion, bool IsOverridden);

/// <summary>
/// The app's state beyond the profiles: which page is open, what is being asked or reported, and the helper. Every
/// command goes to the daemon and comes back through the <see cref="ProfileStore"/>. All of it belongs to the UI
/// thread; commands report their own failures as an <see cref="Alert"/>, so a view can start them and forget them.
/// </summary>
public sealed partial class AppModel : ObservableObject, IAsyncDisposable
{
    private static readonly TimeSpan SettleDuration = TimeSpan.FromSeconds(8);
    private static readonly TimeSpan PollInterval = TimeSpan.FromSeconds(2);
    private static readonly TimeSpan SampleInterval = TimeSpan.FromSeconds(2);

    private readonly IStartupRegistration _startup;
    private readonly ILogger<AppModel> _log;
    private readonly Dictionary<string, ProfileEditor> _editors = [];
    private readonly CancellationTokenSource _lifetime = new();
    private CancellationTokenSource _settling = new();
    private Task _polling = Task.CompletedTask;
    private Task _sampling = Task.CompletedTask;

    /// <summary>Makes the model; nothing is followed until <see cref="Start"/>.</summary>
    public AppModel(
        ProfileStore store,
        HelperInstaller installer,
        IStartupRegistration startup,
        UiText text,
        AppEnvironment environment,
        AppLanguage language,
        TimeProvider time,
        ILogger<AppModel> log)
    {
        Store = store;
        Installer = installer;
        Text = text;
        Language = language;
        Errors = new ErrorText(text);
        Time = time;
        AppVersion = environment.AppVersion;
        IsOverridden = environment.IsOverridden;
        _startup = startup;
        _log = log;
        LaunchAtLogin = startup.IsEnabled;
        Setup = ResolveSetup();
        Store.PropertyChanged += (_, _) => OnInputsChanged();
        Installer.PropertyChanged += (_, _) => OnInputsChanged();
    }

    /// <summary>The daemon's profiles.</summary>
    public ProfileStore Store { get; }

    /// <summary>The helper service.</summary>
    public HelperInstaller Installer { get; }

    /// <summary>The strings.</summary>
    public UiText Text { get; }

    /// <summary>The language of the strings, and the one chosen for the next start.</summary>
    public AppLanguage Language { get; }

    /// <summary>The words for failures.</summary>
    public ErrorText Errors { get; }

    /// <summary>The clock of everything that waits.</summary>
    public TimeProvider Time { get; }

    /// <summary>How fast data moves, from the counters of the profiles.</summary>
    public TrafficHistory Traffic { get; } = new();

    /// <summary>The version of this app, when known.</summary>
    public string? AppVersion { get; }

    /// <summary>The daemon is the developer's: the app does not manage the helper.</summary>
    public bool IsOverridden { get; }

    /// <summary>What the sidebar has selected.</summary>
    [ObservableProperty]
    public partial SidebarItem? Selection { get; set; }

    /// <summary>The page of a profile that is open; it stays when another profile is selected.</summary>
    [ObservableProperty]
    public partial ProfileSection ProfileSection { get; set; } = ProfileSection.Overview;

    /// <summary>The page of Diagnostics that is open.</summary>
    [ObservableProperty]
    public partial DiagnosticsPage DiagnosticsPage { get; set; } = DiagnosticsPage.Overview;

    /// <summary>Counts the requests to search: the log on screen moves the focus to its search field.</summary>
    [ObservableProperty]
    public partial int SearchRequest { get; private set; }

    /// <summary>What came of the last import, when it is worth showing.</summary>
    [ObservableProperty]
    public partial ImportReport? ImportReport { get; set; }

    /// <summary>What the last command that failed says.</summary>
    [ObservableProperty]
    public partial AppAlert? Alert { get; set; }

    /// <summary>The profile whose deletion waits for the user's confirmation.</summary>
    [ObservableProperty]
    public partial string? PendingDeletion { get; set; }

    /// <summary>Removing the helper waits for the user's confirmation.</summary>
    [ObservableProperty]
    public partial bool IsConfirmingUninstall { get; set; }

    /// <summary>Registering the helper again, which drops every tunnel, waits for the user's confirmation.</summary>
    [ObservableProperty]
    public partial bool IsConfirmingReinstall { get; set; }

    /// <summary>The helper was just installed, started or retried and may still be coming up.</summary>
    [ObservableProperty]
    public partial bool IsSettling { get; private set; }

    /// <summary>Whether the app starts when the user signs in.</summary>
    [ObservableProperty]
    public partial bool LaunchAtLogin { get; private set; }

    /// <summary>Where the helper stands.</summary>
    [ObservableProperty]
    public partial DaemonSetup Setup { get; private set; }

    /// <summary>
    /// The helper stopped answering after it had shown its profiles: they stay in the window (with a note) instead of
    /// giving way to a setup page, which a restart would flash.
    /// </summary>
    public bool KeepsProfilesInView => Setup.Kind is SetupKind.NotResponding or SetupKind.Connecting && Store.Profiles.Count > 0;

    /// <summary>
    /// A daemon answers although the service is not registered: someone started it by hand (the developer's console
    /// daemon). Registering it here would put a second one next to it, so the app leaves it alone.
    /// </summary>
    public bool IsHelperExternal => !IsOverridden && Store.Connection == DaemonConnection.Connected && Installer.Status.State == HelperState.NotInstalled;

    /// <summary>
    /// The profiles the helper keeps switched on, whether or not the app runs; none while the helper does not answer,
    /// because what the profiles show is then stale.
    /// </summary>
    public IReadOnlyList<Profile> SwitchedOnProfiles => Setup.IsUsable ? [.. Store.Profiles.Where(profile => profile.DesiredEnabled)] : [];

    /// <summary>Profile text was changed in an editor and not saved.</summary>
    public bool HasUnsavedEdits => _editors.Values.Any(editor => editor.IsDirty);

    /// <summary>The profile the sidebar has selected, if it still exists.</summary>
    public Profile? SelectedProfile => Selection is SidebarItem.ProfileItem item ? Store.Find(item.Id) : null;

    /// <summary>Starts following the daemon and the helper.</summary>
    public void Start()
    {
        Store.Start();
        _sampling = SampleTrafficWhileIdleAsync(_lifetime.Token);
        _polling = PollHelperStatusAsync(_lifetime.Token);
        RecordTraffic();
        ReconcileSelection();
    }

    /// <summary>Ends everything the model started.</summary>
    public async ValueTask DisposeAsync()
    {
        await _lifetime.CancelAsync();
        await _settling.CancelAsync();
        await Task.WhenAll(_polling, _sampling);
        await Store.StopAsync();
        _settling.Dispose();
        _lifetime.Dispose();
    }

    /// <summary>The name of a profile, or null when it is gone.</summary>
    public string? ProfileName(string id) => Store.Find(id)?.Name;

    /// <summary>The editor of a profile's text; made when first asked for and kept until the profile is gone.</summary>
    public ProfileEditor EditorFor(Profile profile)
    {
        if (!_editors.TryGetValue(profile.Id, out var editor))
        {
            editor = new ProfileEditor(profile.Id, profile.Kind, Errors, Text);
            _editors[profile.Id] = editor;
        }

        return editor;
    }

    /// <summary>Asks the log on screen to take the focus to its search field.</summary>
    public void RequestSearch() => SearchRequest++;

    /// <summary>
    /// Keeps something selected while there is something to select: after a deletion the first profile, and the first
    /// profile at launch.
    /// </summary>
    public void ReconcileSelection()
    {
        var existing = Store.Profiles.Select(profile => profile.Id).ToHashSet();
        foreach (var gone in _editors.Keys.Where(id => !existing.Contains(id)).ToList())
        {
            _editors.Remove(gone);
        }

        if (Selection is SidebarItem.DiagnosticsItem or SidebarItem.SettingsItem
            || (Selection is SidebarItem.ProfileItem item && existing.Contains(item.Id)))
        {
            return;
        }

        Selection = Store.Profiles.Count > 0 ? SidebarItem.Profile(Store.Profiles[0].Id) : null;
    }

    private void OnInputsChanged()
    {
        RefreshSetup();
        RecordTraffic();
        if (Store.Profiles.Count > 0 || Selection is SidebarItem.ProfileItem)
        {
            ReconcileSelection();
        }

        OnPropertyChanged(nameof(IsHelperExternal));
        OnPropertyChanged(nameof(SwitchedOnProfiles));
        OnPropertyChanged(nameof(SelectedProfile));
        OnPropertyChanged(nameof(KeepsProfilesInView));
    }

    private void RefreshSetup() => Setup = ResolveSetup();

    private DaemonSetup ResolveSetup() => DaemonSetup.From(new SetupInputs(
        Store.Connection, Store.UnavailableCause, Installer.Status, Store.DaemonInfo?.Version, AppVersion, IsOverridden, IsSettling));

    partial void OnIsSettlingChanged(bool value) => RefreshSetup();

    partial void OnSelectionChanged(SidebarItem? value) => OnPropertyChanged(nameof(SelectedProfile));

    private void RecordTraffic() => Traffic.Record(Store.Profiles, Time.GetUtcNow());

    /// <summary>
    /// The daemon reports a profile only when something about it changes: a tunnel that has gone quiet sends nothing,
    /// and would keep its last rate and draw a line across the silence.
    /// </summary>
    private async Task SampleTrafficWhileIdleAsync(CancellationToken cancellationToken)
    {
        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                await Task.Delay(SampleInterval, Time, cancellationToken);
                RecordTraffic();
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // The app is closing.
        }
    }

    /// <summary>A user who approves or starts the helper outside the app comes back to the app; nothing tells it, so it looks.</summary>
    private async Task PollHelperStatusAsync(CancellationToken cancellationToken)
    {
        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                await Task.Delay(PollInterval, Time, cancellationToken);
                PollHelperOnce();
            }
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
            // The app is closing.
        }
    }

    /// <summary>One look at the helper, as the poll makes it: only while the daemon does not answer.</summary>
    internal void PollHelperOnce()
    {
        if (Store.Connection == DaemonConnection.Connected)
        {
            return;
        }

        var before = Installer.Status.State;
        Installer.Refresh();
        if (before != HelperState.Running && Installer.Status.State == HelperState.Running)
        {
            BeginSettling();
        }
    }
}
