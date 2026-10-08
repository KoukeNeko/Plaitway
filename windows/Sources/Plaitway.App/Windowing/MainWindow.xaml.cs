using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Plaitway.App.Dialogs;
using Plaitway.App.Diagnostics;
using Plaitway.App.Startup;
using Plaitway.App.Views;
using Plaitway.AppCore;
using Plaitway.AppCore.ViewModels;
using WinRT.Interop;

namespace Plaitway.App.Windowing;

/// <summary>
/// The window: Mica behind the shell, the place and size it had, and a close button that hides it, because the helper
/// keeps the profiles connected and the notification-area icon is where the app is while no window is open.
/// </summary>
internal sealed partial class MainWindow : Window, IDialogHost
{
    private const double MinimumWidthDips = 720;
    private const double MinimumHeightDips = 480;

    private readonly ShellViewModel _shell;
    private readonly WindowPlacement _placement;
    // The system holds the window procedure of this subclass for as long as the window lives, so the object must too.
    private readonly WindowMinimumSize _minimumSize;
    private readonly RunReport _report;
    private readonly DateTime _processStart = System.Diagnostics.Process.GetCurrentProcess().StartTime;
    private bool _firstActivationReported;
    private bool _quitting;

    public MainWindow(ShellViewModel shell, AppOptions options, string iconPath, WindowPlacementStore placementStore, RunReport report)
    {
        _shell = shell;
        _report = report;
        InitializeComponent();
        Handle = WindowNative.GetWindowHandle(this);
        Root.Children.Add(new ShellView(shell));
        Title = AppIdentity.ProductName;
        AppWindow.SetIcon(iconPath);
        _minimumSize = new WindowMinimumSize(Handle, MinimumWidthDips, MinimumHeightDips);
        _placement = new WindowPlacement(AppWindow, Handle, placementStore, DispatcherQueue);
        _placement.Restore(options.Bounds);
        _placement.Track();
        ApplyOptions(options);
        AppWindow.Closing += OnClosing;
        Activated += OnActivated;
        Root.Loaded += (_, _) => ApplyTitleBarTheme();
        Root.ActualThemeChanged += (_, _) => ApplyTitleBarTheme();
    }

    /// <summary>The window's handle, for the dialogs the system shows and the scripted runs that look at the window.</summary>
    public nint Handle { get; }

    /// <summary>Lets the window close for real; until then closing it only hides it.</summary>
    public void AllowClose() => _quitting = true;

    /// <summary>Shows the window and brings it to the front.</summary>
    public void Show()
    {
        AppWindow.Show();
        _shell.SetWindowVisible(isVisible: true);
        Activate();
        Win32Window.BringToFront(Handle);
    }

    /// <summary>Saves where the window is, as the app ends.</summary>
    public void SaveLayout() => _placement.Save();

    /// <inheritdoc />
    public Task<FrameworkElement> ShowAsync()
    {
        Show();
        return Task.FromResult<FrameworkElement>(Root);
    }

    private void ApplyOptions(AppOptions options)
    {
        if (options.Theme is { } theme)
        {
            Root.RequestedTheme = theme.Equals("dark", StringComparison.OrdinalIgnoreCase) ? ElementTheme.Dark : ElementTheme.Light;
        }

        if (options.AlwaysOnTop && AppWindow.Presenter is OverlappedPresenter presenter)
        {
            presenter.IsAlwaysOnTop = true;
        }
    }

    private void ApplyTitleBarTheme() => Win32Window.UseDarkTitleBar(Handle, Root.ActualTheme == ElementTheme.Dark);

    private void OnActivated(object sender, WindowActivatedEventArgs args)
    {
        if (_firstActivationReported)
        {
            return;
        }

        _firstActivationReported = true;
        _report.Write("first_activated_ms", (long)(DateTime.Now - _processStart).TotalMilliseconds);
    }

    /// <summary>Closing the window leaves the app in the notification area, where the icon's menu quits it.</summary>
    private void OnClosing(AppWindow sender, AppWindowClosingEventArgs args)
    {
        if (_quitting)
        {
            return;
        }

        args.Cancel = true;
        _placement.Save();
        sender.Hide();
        _shell.SetWindowVisible(isVisible: false);
    }
}
