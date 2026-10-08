using System;
using System.Drawing;
using System.Drawing.Imaging;
using System.Runtime.InteropServices;

// Win32 helpers for the scripts that look at the running app: window capture, window messages, and a key press that is only\n// sent while the app is the window that has the keyboard (a key that goes to another program is a key in the wrong place).\n// Compiled by Add-Type in the script; not part of the app.
public static class UiNative
{
    [StructLayout(LayoutKind.Sequential)]
    public struct Rect { public int Left, Top, Right, Bottom; }

    [StructLayout(LayoutKind.Sequential)]
    struct KeyboardInput { public ushort wVk, wScan; public uint dwFlags, time; public IntPtr dwExtraInfo; }

    [StructLayout(LayoutKind.Explicit, Size = 40)]
    struct Input
    {
        [FieldOffset(0)] public uint type;
        [FieldOffset(8)] public KeyboardInput keyboard;
    }

    const uint InputKeyboard = 1;
    const uint KeyUp = 0x2;
    const uint PrintFullContent = 0x2;
    static readonly IntPtr PerMonitorV2 = new IntPtr(-4);

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    struct NotifyIconData
    {
        public uint cbSize; public IntPtr hWnd; public uint uID, uFlags, uCallbackMessage; public IntPtr hIcon;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string szTip;
        public uint dwState, dwStateMask;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 256)] public string szInfo;
        public uint uVersion;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string szInfoTitle;
        public uint dwInfoFlags; public Guid guidItem; public IntPtr hBalloonIcon;
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)] static extern bool Shell_NotifyIcon(uint message, ref NotifyIconData data);

    /// <summary>Takes the icon of another process out of the notification area, as an Explorer restart does.</summary>
    public static bool DeleteTrayIcon(IntPtr window, uint id)
    {
        var data = new NotifyIconData { cbSize = (uint)Marshal.SizeOf(typeof(NotifyIconData)), hWnd = window, uID = id };
        return Shell_NotifyIcon(0x2, ref data);
    }

    [DllImport("user32.dll")] static extern bool PrintWindow(IntPtr window, IntPtr dc, uint flags);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr window, out Rect rect);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr window);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr window, uint message, UIntPtr wParam, IntPtr lParam);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern uint RegisterWindowMessage(string name);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr window);
    [DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr window);
    [DllImport("user32.dll")] static extern bool SetProcessDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr window, out uint processId);
    [DllImport("user32.dll")] static extern uint SendInput(uint count, Input[] inputs, int size);

    public static void MakeDpiAware() { SetProcessDpiAwarenessContext(PerMonitorV2); }

    public static void CaptureWindow(IntPtr window, string path)
    {
        Rect rect;
        GetWindowRect(window, out rect);
        using (var bitmap = new Bitmap(rect.Right - rect.Left, rect.Bottom - rect.Top, PixelFormat.Format32bppArgb))
        {
            using (var graphics = Graphics.FromImage(bitmap))
            {
                var dc = graphics.GetHdc();
                try { PrintWindow(window, dc, PrintFullContent); }
                finally { graphics.ReleaseHdc(dc); }
            }
            bitmap.Save(path, ImageFormat.Png);
        }
    }

    public static void CaptureScreenRegion(int x, int y, int width, int height, string path)
    {
        using (var bitmap = new Bitmap(width, height, PixelFormat.Format32bppArgb))
        {
            using (var graphics = Graphics.FromImage(bitmap))
            {
                graphics.CopyFromScreen(x, y, 0, 0, new Size(width, height));
            }
            bitmap.Save(path, ImageFormat.Png);
        }
    }

    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern IntPtr FindWindow(string className, string windowName);

    /// <summary>Captures the open popup menu (window class #32768) and nothing around it; false when no menu is open.</summary>
    public static bool CaptureOpenMenu(string path)
    {
        var menu = FindWindow("#32768", null);
        if (menu == IntPtr.Zero) return false;
        Rect rect;
        GetWindowRect(menu, out rect);
        CaptureScreenRegion(rect.Left, rect.Top, rect.Right - rect.Left, rect.Bottom - rect.Top, path);
        return true;
    }

    public static uint MakeLong(int low, int high) { return (uint)((low & 0xFFFF) | ((high & 0xFFFF) << 16)); }

    /// <summary>The window that has the keyboard belongs to this process.</summary>
    public static bool HasKeyboard(int processId)
    {
        uint owner;
        GetWindowThreadProcessId(GetForegroundWindow(), out owner);
        return owner == (uint)processId;
    }

    /// <summary>
    /// Presses a key, but only while a window of the process has the keyboard: a menu or a dialog of the app counts, another
    /// program does not. Nothing is sent otherwise, and the script is told so.
    /// </summary>
    public static void PressVirtualKey(int processId, ushort key)
    {
        if (!HasKeyboard(processId))
            throw new InvalidOperationException("the app does not have the keyboard, so the key was not sent");
        Send(new Input { type = InputKeyboard, keyboard = new KeyboardInput { wVk = key } });
        Send(new Input { type = InputKeyboard, keyboard = new KeyboardInput { wVk = key, dwFlags = KeyUp } });
    }
    static void Send(Input input)
    {
        if (SendInput(1, new[] { input }, Marshal.SizeOf(typeof(Input))) != 1)
            throw new InvalidOperationException("SendInput was refused (error " + Marshal.GetLastWin32Error() + ")");
    }
}
