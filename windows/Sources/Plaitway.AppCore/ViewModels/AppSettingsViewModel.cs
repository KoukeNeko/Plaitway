using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>The app's own settings and the helper: the page that Settings opens.</summary>
public sealed partial class AppSettingsViewModel : ObservableObject, IPageLifecycle, IDisposable
{
    private bool _isRefreshing;

    private readonly AppModel _model;

    /// <summary>Makes the page.</summary>
    public AppSettingsViewModel(AppModel model)
    {
        _model = model;
        _model.PropertyChanged += OnChanged;
        _model.Store.PropertyChanged += OnChanged;
        _model.Installer.PropertyChanged += OnChanged;
        Refresh();
    }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>Whether the app starts when the user signs in; the view writes it as the user switches it.</summary>
    [ObservableProperty]
    public partial bool LaunchAtLogin { get; set; }

    /// <summary>The app can add itself to the startup list.</summary>
    public bool CanSetLaunchAtLogin => _model.CanSetLaunchAtLogin;

    /// <summary>The state of the service, the versions, the engines; only what is known.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<DetailRow> Helper { get; private set; } = [];

    /// <summary>The app may install, restart and remove the helper: it is its own, not the developer's and not started by hand.</summary>
    [ObservableProperty]
    public partial bool CanManageHelper { get; private set; }

    /// <summary>The service is installed and not running, so it can be started.</summary>
    [ObservableProperty]
    public partial bool CanStartHelper { get; private set; }

    /// <summary>The service is installed, so it can be removed.</summary>
    [ObservableProperty]
    public partial bool CanUninstallHelper { get; private set; }

    /// <inheritdoc />
    public void Activate() => _model.Installer.Refresh();

    /// <inheritdoc />
    public void Deactivate()
    {
    }

    /// <inheritdoc />
    public void Dispose()
    {
        _model.PropertyChanged -= OnChanged;
        _model.Store.PropertyChanged -= OnChanged;
        _model.Installer.PropertyChanged -= OnChanged;
    }

    /// <summary>Starts the service.</summary>
    [RelayCommand]
    public Task StartHelperAsync() => _model.StartHelperAsync();

    /// <summary>Asks whether to register the service again.</summary>
    [RelayCommand]
    public void RequestReinstall() => _model.IsConfirmingReinstall = true;

    /// <summary>Asks whether to remove the service.</summary>
    [RelayCommand]
    public void RequestUninstall() => _model.IsConfirmingUninstall = true;

    partial void OnLaunchAtLoginChanged(bool value)
    {
        if (!_isRefreshing)
        {
            _model.SetLaunchAtLogin(value);
        }
    }

    private void OnChanged(object? sender, PropertyChangedEventArgs args) => Refresh();

    private void Refresh()
    {
        _isRefreshing = true;
        try
        {
            LaunchAtLogin = _model.LaunchAtLogin;
            var status = _model.Installer.Status;
            CanManageHelper = !_model.IsOverridden && !_model.IsHelperExternal && status.HasExecutable;
            CanStartHelper = CanManageHelper && status.State == HelperState.Stopped;
            CanUninstallHelper = CanManageHelper && status.State != HelperState.NotInstalled;
            Helper = Rows(status);
        }
        finally
        {
            _isRefreshing = false;
        }
    }

    private List<DetailRow> Rows(HelperStatus status)
    {
        var rows = new List<DetailRow>
        {
            new(Text.Status, _model.IsHelperExternal ? Text.InstalledExternally : StateLabel(status.State)),
        };
        if (_model.Store.DaemonInfo is { } info)
        {
            rows.Add(new DetailRow(Text.HelperVersion, info.Version));
            rows.AddRange(info.Engines.Select(engine => new DetailRow(DiagnosticsReport.EngineName(engine.Kind), EngineText(engine))));
            if (!info.Privileged)
            {
                rows.Add(new DetailRow(Text.Privileges, Text.Limited));
            }
        }

        if (_model.AppVersion is { } appVersion)
        {
            rows.Add(new DetailRow(Text.AppVersion, appVersion));
        }

        return rows;
    }

    private string StateLabel(HelperState state) => state switch
    {
        HelperState.NotInstalled => Text.NotInstalled,
        HelperState.Stopped => Text.Stopped,
        HelperState.Starting => Text.Starting,
        HelperState.Running => Text.Running,
        HelperState.Stopping => Text.Stopping,
        _ => Text.Unknown,
    };

    /// <summary>The engine's version, or why it cannot run.</summary>
    private string EngineText(EngineInfo engine) =>
        engine.Available ? engine.Version : engine.Detail.Length == 0 ? Text.Unavailable : engine.Detail;
}
