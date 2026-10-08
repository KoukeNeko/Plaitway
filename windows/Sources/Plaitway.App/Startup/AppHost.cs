using System.Diagnostics.CodeAnalysis;
using System.Reflection;
using Microsoft.UI.Dispatching;
using Plaitway.App.Dialogs;
using Plaitway.App.Diagnostics;
using Plaitway.App.Platform;
using Plaitway.App.Tray;
using Plaitway.App.Windowing;
using Plaitway.AppCore;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Dialogs;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Text;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client;
using Plaitway.Client.Storage;

namespace Plaitway.App.Startup;

/// <summary>
/// Puts the app together: the client of the daemon, the models, the window, the notification-area icon, and what ties them to
/// each other. Everything the window shows is made from here, and nothing else makes anything: this is the one place that
/// knows which implementation of each service the app runs on.
/// </summary>
[SuppressMessage("Design", "CA1001", Justification = "The host lives as long as the process; ShutDownAsync disposes what it owns before the process ends.")]
internal sealed class AppHost : IDialogHost
{
    private const string IconFileName = "Plaitway.ico";
    private const string AssetsFolder = "Assets";
    private const string DataFolderName = "Plaitway";
    private const string LogFileName = "Logs\\app.log";
    private const string PlacementFileName = "window.json";

    private readonly Microsoft.UI.Xaml.Application _application;
    private readonly DispatcherQueue _dispatcher;
    private readonly RunReport _report;
    private readonly SingleInstance _instance;
    private readonly DaemonClientApi _api;
    private readonly AppModel _model;
    private readonly ShellViewModel _shell;
    private readonly DialogCoordinator _coordinator;
    private readonly UiText _text;
    private readonly TrayIconSet _trayIcons;
    private readonly DialogHostProxy _dialogHost;
    private MainWindow? _window;
    private TrayIcon? _tray;
    private bool _isPoweringOff;
    private bool _isQuitting;

    private AppHost(Microsoft.UI.Xaml.Application application, AppOptions options, DaemonClientApi api, SingleInstance instance, RunReport report)
    {
        _application = application;
        _dispatcher = DispatcherQueue.GetForCurrentThread();
        _report = report;
        _instance = instance;
        _api = api;
        var environment = DaemonLocation.CurrentEnvironment();
        var isOverridden = DaemonLocation.Override(environment) is not null;
        var dataDirectory = options.DataDirectory ?? Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), DataFolderName);
        var logs = new FileLoggerProvider(Path.Combine(dataDirectory, LogFileName));
        _text = new UiText(new MrtLocalizer(options.Language));
        var scheduler = new DispatcherScheduler(_dispatcher);

        // A daemon behind PLAITWAY_SOCKET (debug builds) is not the helper: the Credential Manager is neither read to answer it
        // nor cleaned up on its word, and the app cannot ask Windows to change the helper on its behalf.
        ICredentialStore credentials = isOverridden ? new InMemoryCredentialStore() : new WindowsCredentialStore();
        IElevatedLauncher launcher = isOverridden ? new DisabledElevatedLauncher() : new ShellElevatedLauncher();
        var store = new ProfileStore(api, credentials, scheduler, logs.CreateLogger<ProfileStore>());
        var locator = new HelperLocator(AppContext.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar), File.Exists);
        var installer = new HelperInstaller(new ScmHelperService(), launcher, locator);
        var startup = new RunKeyStartup(Environment.ProcessPath ?? string.Empty, isAvailable: !DaemonLocation.OverrideEnabled);
        _model = new AppModel(store, installer, startup, _text, new AppEnvironment(AppVersion(), isOverridden), TimeProvider.System, logs.CreateLogger<AppModel>());

        _dialogHost = new DialogHostProxy();
        var dialogs = _dialogService = new ContentDialogService(_dialogHost);
        _shell = new ShellViewModel(_model, new WindowsClipboard(), new WindowsFilePicker(() => _window?.Handle ?? nint.Zero), dialogs);
        _coordinator = new DialogCoordinator(_model, dialogs);
        _trayIcons = new TrayIconSet(Path.Combine(AppContext.BaseDirectory, AssetsFolder));
        _quitPrompts = new QuitPrompts(_text, dialogs);
        Options = options;
        PlacementPath = Path.Combine(dataDirectory, PlacementFileName);
    }

    private readonly QuitPrompts _quitPrompts;
    private readonly ContentDialogService _dialogService;

    /// <summary>What the command line and the environment said.</summary>
    public AppOptions Options { get; }

    private string PlacementPath { get; }

    /// <summary>
    /// Starts the app, or hands over to the copy that is running: null then, and the process ends. A pipe the environment names
    /// that is not a local named pipe stops the app rather than send passwords to a path nobody checked (see
    /// <see cref="DaemonLocation"/>).
    /// </summary>
    public static AppHost? Start(Microsoft.UI.Xaml.Application application, AppOptions options, RunReport report)
    {
        var environment = DaemonLocation.CurrentEnvironment();
        var pipe = DaemonLocation.Pipe(environment);
        var isOverridden = DaemonLocation.Override(environment) is not null;
        var instance = SingleInstance.Acquire(SingleInstance.KeyFor(isOverridden ? pipe.FullPath : null));
        if (!instance.IsPrimary)
        {
            instance.SignalPrimary();
            instance.Dispose();
            return null;
        }

        var api = new DaemonClientApi(DaemonClient.Connect(pipe));
        var host = new AppHost(application, options, api, instance, report);
        host.Run();
        return host;
    }

    /// <inheritdoc />
    public Task<Microsoft.UI.Xaml.FrameworkElement> ShowAsync() => _window!.ShowAsync();

    private static string? AppVersion() =>
        typeof(AppHost).Assembly.GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion;

    private void Run()
    {
        var assets = Path.Combine(AppContext.BaseDirectory, AssetsFolder);
        _window = new MainWindow(_shell, Options, Path.Combine(assets, IconFileName), new WindowPlacementStore(PlacementPath), _report);
        _dialogHost.Target = _window;
        CreateTray();
        _instance.ActivationRequested += () => _dispatcher.TryEnqueue(() => _window.Show());
        _shell.ShowWindowRequested += () => _window.Show();
        _shell.ImportRequested += () => _shell.ImportCommand.Execute(null);
        _shell.QuitRequested += () => _ = QuitAsync();
        _instance.Listen();
        _model.Start();
        _coordinator.Start();
        if (Options.StartHidden)
        {
            _shell.SetWindowVisible(isVisible: false);
        }
        else
        {
            _window.Show();
        }
    }

    private void CreateTray()
    {
        _tray = new TrayIcon(_shell.Tray.Tooltip, _trayIcons.IconOf(_shell.Tray.State), _report);
        _tray.Selected += _shell.Tray.Select;
        _tray.EntryChosen += entry => _ = _shell.Tray.InvokeAsync(entry);
        _tray.SessionEnding += () => _isPoweringOff = true;
        _tray.AppearanceChanged += () =>
        {
            _trayIcons.Reload();
            UpdateTray();
        };
        _shell.Tray.PropertyChanged += (_, _) => UpdateTray();
        UpdateTray();
        _report.Write("tray_window", _tray.WindowHandle);
    }

    private void UpdateTray() => _tray?.Update(_shell.Tray.Tooltip, _trayIcons.IconOf(_shell.Tray.State), _shell.Tray.Entries);

    /// <summary>
    /// Ends the app once the user has said what the profiles that are on should do; a user who cancels keeps it. Windows
    /// signing out or shutting down does not wait for an answer.
    /// </summary>
    private async Task QuitAsync()
    {
        if (_isQuitting)
        {
            return;
        }

        _isQuitting = true;
        try
        {
            var decision = await _model.RequestQuitAsync(_quitPrompts, _isPoweringOff);
            if (decision.ShowWindow)
            {
                _window?.Show();
            }

            if (decision.CanQuit)
            {
                await ShutDownAsync();
            }
        }
        finally
        {
            _isQuitting = false;
        }
    }

    /// <summary>Stops everything the app started, so that the process ends by itself.</summary>
    private async Task ShutDownAsync()
    {
        _tray?.Dispose();
        _tray = null;
        _trayIcons.Dispose();
        _coordinator.Dispose();
        _shell.Dispose();
        await _model.DisposeAsync();
        await _api.DisposeAsync();
        _window?.SaveLayout();
        _window?.AllowClose();
        _dialogService.Dispose();
        _instance.Dispose();
        _report.Write("exit", "clean");
        _window?.Close();
        _application.Exit();
    }

    /// <summary>The window that dialogs are made for is made after the services that need it.</summary>
    private sealed class DialogHostProxy : IDialogHost
    {
        public MainWindow? Target { get; set; }

        public Task<Microsoft.UI.Xaml.FrameworkElement> ShowAsync() =>
            Target?.ShowAsync() ?? throw new InvalidOperationException("the window is not there yet");
    }
}
