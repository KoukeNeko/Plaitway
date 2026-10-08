using Microsoft.Win32;
using Plaitway.AppCore.Presentation;

namespace Plaitway.App.Tray;

/// <summary>
/// The notification-area icons: one file per state of the icon and per colour of the taskbar, each holding the sizes of
/// 100 % to 300 % scaling (<c>scripts\generate-icons.ps1</c> makes them). The set loads the size that fits the scale of the
/// display now and the colour of the taskbar now; it is loaded again when either changes.
/// </summary>
internal sealed class TrayIconSet : IDisposable
{
    private const string FolderName = "Tray";
    private const int SmallIconWidthMetric = 49;
    private const string PersonalizeKey = @"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize";
    private const string TaskbarThemeValue = "SystemUsesLightTheme";

    private readonly string _directory;
    private readonly Dictionary<AggregateState, nint> _icons = [];

    /// <param name="assetsDirectory">The folder with <c>Tray\*.ico</c>; the app's own, next to its executable.</param>
    public TrayIconSet(string assetsDirectory)
    {
        _directory = Path.Combine(assetsDirectory, FolderName);
        Load();
    }

    /// <summary>The icon of the state, in the size and for the taskbar of the last load.</summary>
    public nint IconOf(AggregateState state) => _icons[state];

    /// <summary>Loads the icons again for the display and the taskbar as they are now.</summary>
    public void Reload()
    {
        Release();
        Load();
    }

    /// <inheritdoc />
    public void Dispose() => Release();

    /// <summary>The name of the icon file: the look of the state, and the taskbar it is drawn for.</summary>
    internal static string FileNameOf(AggregateState state, bool lightTaskbar) =>
        $"tray-{LookOf(state)}-{(lightTaskbar ? "light" : "dark")}.ico";

    private static string LookOf(AggregateState state) => state switch
    {
        AggregateState.Idle => "idle",
        AggregateState.Connecting => "connecting",
        AggregateState.Connected => "connected",
        _ => "attention",
    };

    private static bool TaskbarIsLight()
    {
        using var key = Registry.CurrentUser.OpenSubKey(PersonalizeKey);
        return key?.GetValue(TaskbarThemeValue) is int value && value != 0;
    }

    private void Load()
    {
        var size = TrayNative.GetSystemMetricsForDpi(SmallIconWidthMetric, TrayNative.GetDpiForSystem());
        var light = TaskbarIsLight();
        foreach (var state in Enum.GetValues<AggregateState>())
        {
            var path = Path.Combine(_directory, FileNameOf(state, light));
            var icon = TrayNative.LoadImage(nint.Zero, path, TrayNative.ImageIcon, size, size, TrayNative.LrLoadFromFile);
            _icons[state] = icon != nint.Zero
                ? icon
                : throw new FileNotFoundException($"cannot load the tray icon {path}: error {System.Runtime.InteropServices.Marshal.GetLastPInvokeError()}", path);
        }
    }

    private void Release()
    {
        foreach (var icon in _icons.Values)
        {
            TrayNative.DestroyIcon(icon);
        }

        _icons.Clear();
    }
}
