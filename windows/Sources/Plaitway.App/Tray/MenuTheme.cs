using System.Runtime.InteropServices;

namespace Plaitway.App.Tray;

/// <summary>
/// Lets the native menus of this process follow the system's dark mode. Windows has no documented call for
/// it: the two functions are exported by uxtheme.dll by ordinal only, have been there since Windows 10
/// 1903, and are what WinForms uses for its own dark mode. When they are missing, menus stay light.
/// </summary>
internal static unsafe partial class MenuTheme
{
    private const string UxThemeLibrary = "uxtheme.dll";
    private const nint SetPreferredAppModeOrdinal = 135;
    private const nint FlushMenuThemesOrdinal = 136;
    private const int AllowDarkMode = 1;

    /// <summary>Asks the system to draw this process's native menus dark when the system is.</summary>
    public static void FollowSystem()
    {
        var library = LoadLibrary(UxThemeLibrary);
        if (library == nint.Zero)
        {
            return;
        }

        var setPreferredAppMode = GetProcAddress(library, SetPreferredAppModeOrdinal);
        var flushMenuThemes = GetProcAddress(library, FlushMenuThemesOrdinal);
        if (setPreferredAppMode == nint.Zero || flushMenuThemes == nint.Zero)
        {
            return;
        }

        ((delegate* unmanaged<int, int>)setPreferredAppMode)(AllowDarkMode);
        ((delegate* unmanaged<void>)flushMenuThemes)();
    }

    [LibraryImport("kernel32.dll", EntryPoint = "LoadLibraryW", StringMarshalling = StringMarshalling.Utf16)]
    private static partial nint LoadLibrary(string name);

    [LibraryImport("kernel32.dll")]
    private static partial nint GetProcAddress(nint module, nint ordinal);
}
