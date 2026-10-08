using System.Runtime.InteropServices;

namespace Plaitway.App.Tray;

/// <summary>The Win32 calls behind the notification-area icon: its message window, the icon itself and its menu.</summary>
internal static unsafe partial class TrayNative
{
    // Shell_NotifyIcon commands and flags.
    public const uint NimAdd = 0x0;
    public const uint NimModify = 0x1;
    public const uint NimDelete = 0x2;
    public const uint NimSetVersion = 0x4;
    public const uint NifMessage = 0x1;
    public const uint NifIcon = 0x2;
    public const uint NifTip = 0x4;
    public const uint NifShowTip = 0x80;

    /// <summary>NOTIFYICON_VERSION_4: the callback says what happened in the low word of lParam and where in wParam.</summary>
    public const uint NotifyIconVersion4 = 4;

    // Window messages.
    public const uint WmNull = 0x0;
    public const uint WmQueryEndSession = 0x11;
    public const uint WmEndSession = 0x16;
    public const uint WmSettingChange = 0x1A;
    public const uint WmDisplayChange = 0x7E;
    public const uint WmDpiChanged = 0x2E0;
    public const uint WmApp = 0x8000;
    public const uint WmContextMenu = 0x7B;
    public const uint WmUser = 0x400;
    public const uint NinSelect = WmUser;
    public const uint NinKeySelect = WmUser + 1;

    // Menus.
    public const uint MiimState = 0x1;
    public const uint MiimId = 0x2;
    public const uint MiimString = 0x40;
    public const uint MiimFType = 0x100;
    public const uint MftString = 0x0;
    public const uint MftSeparator = 0x800;
    public const uint MftRadioCheck = 0x200;
    public const uint MfsGrayed = 0x3;
    public const uint MfsChecked = 0x8;
    public const uint MfsDefault = 0x1000;
    public const uint MfString = 0x0;
    public const uint MfSeparator = 0x800;
    public const uint TpmReturnCommand = 0x100;
    public const uint TpmRightButton = 0x2;
    public const uint TpmBottomAlign = 0x20;
    public const uint TpmNoNotify = 0x80;

    // Icons.
    public const uint ImageIcon = 1;
    public const uint LrLoadFromFile = 0x10;
    public const uint LrDefaultSize = 0x40;

    [StructLayout(LayoutKind.Sequential)]
    public struct Point
    {
        public int X;
        public int Y;
    }

    [StructLayout(LayoutKind.Sequential)]
    public struct NotifyIconData
    {
        public uint Size;
        public nint Window;
        public uint Id;
        public uint Flags;
        public uint CallbackMessage;
        public nint Icon;
        public fixed char Tip[128];
        public uint State;
        public uint StateMask;
        public fixed char Info[256];
        public uint Version;
        public fixed char InfoTitle[64];
        public uint InfoFlags;
        public Guid Item;
        public nint BalloonIcon;
    }

    [StructLayout(LayoutKind.Sequential)]
    public struct MenuItemInfo
    {
        public uint Size;
        public uint Mask;
        public uint Type;
        public uint State;
        public uint Id;
        public nint SubMenu;
        public nint BitmapChecked;
        public nint BitmapUnchecked;
        public nint ItemData;
        public nint TypeData;
        public uint Cch;
        public nint BitmapItem;
    }

    [StructLayout(LayoutKind.Sequential)]
    public struct WindowClass
    {
        public uint Size;
        public uint Style;
        public nint WindowProcedure;
        public int ClassExtra;
        public int WindowExtra;
        public nint Instance;
        public nint Icon;
        public nint Cursor;
        public nint Background;
        public nint MenuName;
        public nint ClassName;
        public nint SmallIcon;
    }

    [LibraryImport("shell32.dll", EntryPoint = "Shell_NotifyIconW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool ShellNotifyIcon(uint message, ref NotifyIconData data);

    [LibraryImport("user32.dll", EntryPoint = "RegisterClassExW", SetLastError = true)]
    public static partial ushort RegisterClass(ref WindowClass windowClass);

    [LibraryImport("user32.dll", EntryPoint = "UnregisterClassW", StringMarshalling = StringMarshalling.Utf16)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool UnregisterClass(string className, nint instance);

    [LibraryImport("user32.dll", EntryPoint = "CreateWindowExW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    public static partial nint CreateWindow(
        uint extendedStyle, string className, string windowName, uint style, int x, int y, int width, int height,
        nint parent, nint menu, nint instance, nint parameter);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool DestroyWindow(nint window);

    [LibraryImport("user32.dll", EntryPoint = "DefWindowProcW")]
    public static partial nint DefaultWindowProcedure(nint window, uint message, nuint wordParameter, nint longParameter);

    [LibraryImport("user32.dll", EntryPoint = "RegisterWindowMessageW", StringMarshalling = StringMarshalling.Utf16)]
    public static partial uint RegisterWindowMessage(string name);

    [LibraryImport("user32.dll", EntryPoint = "PostMessageW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool PostMessage(nint window, uint message, nuint wordParameter, nint longParameter);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool SetForegroundWindow(nint window);

    [LibraryImport("user32.dll")]
    public static partial nint CreatePopupMenu();

    [LibraryImport("user32.dll", EntryPoint = "InsertMenuItemW", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool InsertMenuItem(nint menu, uint position, [MarshalAs(UnmanagedType.Bool)] bool byPosition, ref MenuItemInfo item);

    [LibraryImport("user32.dll")]
    public static partial int GetSystemMetricsForDpi(int index, uint dpi);

    [LibraryImport("user32.dll")]
    public static partial uint GetDpiForSystem();

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool SetMenuDefaultItem(nint menu, uint item, uint byPosition);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool DestroyMenu(nint menu);

    [LibraryImport("user32.dll")]
    public static partial uint TrackPopupMenuEx(nint menu, uint flags, int x, int y, nint window, nint parameters);

    [LibraryImport("user32.dll", EntryPoint = "LoadImageW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    public static partial nint LoadImage(nint instance, string name, uint type, int width, int height, uint flags);

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool DestroyIcon(nint icon);

    [LibraryImport("kernel32.dll", EntryPoint = "GetModuleHandleW", StringMarshalling = StringMarshalling.Utf16)]
    public static partial nint GetModuleHandle(string? name);

    /// <summary>Copies <paramref name="text"/> into a fixed buffer of <paramref name="capacity"/> characters, cut to fit and ended with a null.</summary>
    public static void CopyText(char* destination, int capacity, string text)
    {
        var length = Math.Min(text.Length, capacity - 1);
        text.AsSpan(0, length).CopyTo(new Span<char>(destination, capacity));
        destination[length] = '\0';
    }
}
