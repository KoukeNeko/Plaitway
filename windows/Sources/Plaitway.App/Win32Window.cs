using System.Runtime.InteropServices;

namespace Plaitway.App;

/// <summary>The window calls that WinUI has no API for.</summary>
internal static partial class Win32Window
{
    /// <summary>DWMWA_USE_IMMERSIVE_DARK_MODE: draws the title bar and the frame in the dark theme.</summary>
    private const uint ImmersiveDarkModeAttribute = 20;

    private const uint MonitorDefaultToNearest = 2;
    private const uint EffectiveDpi = 0;
    private const uint DefaultDpi = 96;
    private const uint ForegroundRestore = 9;

    /// <summary>The DPI of the monitor the window is on (96 is 100 %), which follows the window when it is moved.</summary>
    public static uint DpiOf(nint window) => GetDpiForWindow(window);

    /// <summary>The DPI of the monitor that is nearest to a point of the virtual screen, for a window that is not there yet.</summary>
    public static uint DpiAt(int x, int y)
    {
        var monitor = MonitorFromPoint(new Point(x, y), MonitorDefaultToNearest);
        return GetDpiForMonitor(monitor, EffectiveDpi, out var dpiX, out _) == 0 ? dpiX : DefaultDpi;
    }

    /// <summary>
    /// Makes the native title bar follow the theme the content is drawn in. WinUI draws the content in the
    /// app's theme but leaves the title bar to the system's Windows mode, so that a dark app can wear a
    /// light title bar.
    /// </summary>
    public static void UseDarkTitleBar(nint window, bool dark)
    {
        var value = dark ? 1 : 0;
        DwmSetWindowAttribute(window, ImmersiveDarkModeAttribute, ref value, sizeof(int));
    }

    /// <summary>Brings the window to the front and gives it the keyboard; a minimized window is restored first.</summary>
    public static void BringToFront(nint window)
    {
        if (IsIconic(window))
        {
            ShowWindow(window, (int)ForegroundRestore);
        }

        SetForegroundWindow(window);
    }

    [StructLayout(LayoutKind.Sequential)]
    private readonly struct Point(int x, int y)
    {
        public readonly int X = x;
        public readonly int Y = y;
    }

    [LibraryImport("user32.dll")]
    private static partial uint GetDpiForWindow(nint window);

    [LibraryImport("user32.dll")]
    private static partial nint MonitorFromPoint(Point point, uint flags);

    [LibraryImport("shcore.dll")]
    private static partial int GetDpiForMonitor(nint monitor, uint type, out uint dpiX, out uint dpiY);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool IsIconic(nint window);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool ShowWindow(nint window, int command);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetForegroundWindow(nint window);

    [LibraryImport("dwmapi.dll")]
    private static partial int DwmSetWindowAttribute(nint window, uint attribute, ref int value, int size);
}
