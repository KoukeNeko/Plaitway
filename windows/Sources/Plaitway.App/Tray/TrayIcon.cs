using System.Runtime.InteropServices;
using Plaitway.App.Diagnostics;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tray;

namespace Plaitway.App.Tray;

/// <summary>
/// The app's icon in the notification area, with a menu. The shell reports to a window of its own, a hidden top-level
/// window made here (not a message-only window, which gets no broadcasts and so never hears that Explorer started or that
/// the user signs out); the icon is put back when Explorer starts again (the registered <c>TaskbarCreated</c> message),
/// which loses every icon it held.
/// </summary>
/// <remarks>
/// Written against Shell_NotifyIcon directly, not with a NuGet package: the menu is a native menu, which is accessible and
/// themed by the system without a XAML island to host a flyout, and what the icon has to do is small. See
/// <c>windows/docs/ui-toolkit.md</c>.
/// </remarks>
internal sealed unsafe class TrayIcon : IDisposable
{
    private const string WindowClassName = "Plaitway.TrayWindow";
    private const string TaskbarCreatedMessage = "TaskbarCreated";
    private const string ColorSetChange = "ImmersiveColorSet";
    private const uint IconId = 1;
    private const uint CallbackMessage = TrayNative.WmApp + 1;
    private const int TipCapacity = 128;
    private const uint NoCommandChosen = 0;
    private const nint EndSessionCloses = 1;
    private const string ColumnSeparator = "\t";

    private readonly RunReport _report;
    private readonly uint _taskbarCreated;

    // The system holds this function pointer for as long as the window exists, so the delegate must too.
    private readonly WindowProcedure _procedure;
    private readonly nint _instance;
    private IReadOnlyList<TrayEntry> _entries = [];
    private string _tooltip;
    private nint _icon;
    private nint _window;
    private bool _disposed;

    /// <summary>Makes the icon with its first look.</summary>
    /// <param name="tooltip">What the shell shows when the pointer rests on the icon, and what a screen reader calls it.</param>
    /// <param name="icon">The icon, which the caller owns.</param>
    /// <param name="report">For scripted runs.</param>
    public TrayIcon(string tooltip, nint icon, RunReport report)
    {
        _tooltip = tooltip;
        _icon = icon;
        _report = report;
        _procedure = HandleMessage;
        _instance = TrayNative.GetModuleHandle(null);
        _taskbarCreated = TrayNative.RegisterWindowMessage(TaskbarCreatedMessage);
        MenuTheme.FollowSystem();
        CreateWindow();
        AddIcon();
    }

    /// <summary>The user chose an entry of the menu.</summary>
    public event Action<TrayEntry>? EntryChosen;

    /// <summary>The user clicked the icon, or pressed Enter on it.</summary>
    public event Action? Selected;

    /// <summary>The colours of the taskbar or the scale of the display changed: the icon may need another look.</summary>
    public event Action? AppearanceChanged;

    /// <summary>Windows is signing out or shutting down: nothing may wait for an answer from the user.</summary>
    public event Action? SessionEnding;

    /// <summary>How many times the icon has been put in the notification area: once, and again after every Explorer restart.</summary>
    public int AddCount { get; private set; }

    /// <summary>The window that receives the shell's messages, for a scripted run to send to.</summary>
    public nint WindowHandle => _window;

    private delegate nint WindowProcedure(nint window, uint message, nuint wordParameter, nint longParameter);

    /// <summary>Shows what the icon and its menu say now.</summary>
    public void Update(string tooltip, nint icon, IReadOnlyList<TrayEntry> entries)
    {
        _tooltip = tooltip;
        _icon = icon;
        _entries = entries;
        var data = NewData();
        data.Flags = TrayNative.NifIcon | TrayNative.NifTip;
        data.Icon = _icon;
        TrayNative.CopyText(data.Tip, TipCapacity, _tooltip);
        TrayNative.ShellNotifyIcon(TrayNative.NimModify, ref data);
    }

    /// <inheritdoc />
    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }

        _disposed = true;
        var data = NewData();
        TrayNative.ShellNotifyIcon(TrayNative.NimDelete, ref data);
        if (_window != nint.Zero)
        {
            TrayNative.DestroyWindow(_window);
            TrayNative.UnregisterClass(WindowClassName, _instance);
        }
    }

    /// <summary>Tells the shell about the icon. False when the shell refuses; the notification area may not exist yet.</summary>
    private bool AddIcon()
    {
        var data = NewData();
        data.Flags = TrayNative.NifMessage | TrayNative.NifIcon | TrayNative.NifTip | TrayNative.NifShowTip;
        data.CallbackMessage = CallbackMessage;
        data.Icon = _icon;
        TrayNative.CopyText(data.Tip, TipCapacity, _tooltip);
        var added = TrayNative.ShellNotifyIcon(TrayNative.NimAdd, ref data);
        if (added)
        {
            data.Version = TrayNative.NotifyIconVersion4;
            TrayNative.ShellNotifyIcon(TrayNative.NimSetVersion, ref data);
            AddCount++;
        }

        _report.Write("tray_add", $"{AddCount},{added}");
        return added;
    }

    private TrayNative.NotifyIconData NewData() => new()
    {
        Size = (uint)sizeof(TrayNative.NotifyIconData),
        Window = _window,
        Id = IconId,
    };

    private void CreateWindow()
    {
        fixed (char* className = WindowClassName)
        {
            var windowClass = new TrayNative.WindowClass
            {
                Size = (uint)sizeof(TrayNative.WindowClass),
                WindowProcedure = Marshal.GetFunctionPointerForDelegate(_procedure),
                Instance = _instance,
                ClassName = (nint)className,
            };
            if (TrayNative.RegisterClass(ref windowClass) == 0)
            {
                throw new InvalidOperationException($"cannot register the tray window class: error {Marshal.GetLastPInvokeError()}");
            }
        }

        _window = TrayNative.CreateWindow(0, WindowClassName, WindowClassName, 0, 0, 0, 0, 0, nint.Zero, nint.Zero, _instance, nint.Zero);
        if (_window == nint.Zero)
        {
            throw new InvalidOperationException($"cannot create the tray window: error {Marshal.GetLastPInvokeError()}");
        }
    }

    /// <summary>Runs on the thread that made the window. It must not throw: there is native code above it.</summary>
    private nint HandleMessage(nint window, uint message, nuint wordParameter, nint longParameter)
    {
        try
        {
            if (message == _taskbarCreated)
            {
                AddIcon();
                return 0;
            }

            switch (message)
            {
                case CallbackMessage:
                    HandleIconEvent(wordParameter, longParameter);
                    return 0;
                case TrayNative.WmQueryEndSession:
                    SessionEnding?.Invoke();
                    return EndSessionCloses;
                case TrayNative.WmSettingChange when Marshal.PtrToStringUni(longParameter) == ColorSetChange:
                case TrayNative.WmDisplayChange:
                case TrayNative.WmDpiChanged:
                    AppearanceChanged?.Invoke();
                    return 0;
                default:
                    break;
            }
        }
        catch (Exception error) when (error is InvalidOperationException or ExternalException)
        {
            _report.Write("tray_error", error.Message);
        }

        return TrayNative.DefaultWindowProcedure(window, message, wordParameter, longParameter);
    }

    private void HandleIconEvent(nuint wordParameter, nint longParameter)
    {
        switch ((uint)(longParameter & 0xFFFF))
        {
            case TrayNative.WmContextMenu:
                ShowMenu(SignedLow(wordParameter), SignedHigh(wordParameter));
                break;
            case TrayNative.NinSelect or TrayNative.NinKeySelect:
                Selected?.Invoke();
                break;
            default:
                break;
        }
    }

    private static int SignedLow(nuint value) => (short)(value & 0xFFFF);

    private static int SignedHigh(nuint value) => (short)((value >> 16) & 0xFFFF);

    /// <summary>Shows the menu where the icon was clicked and reports the entry chosen, if any.</summary>
    private void ShowMenu(int x, int y)
    {
        var entries = _entries;
        var menu = TrayNative.CreatePopupMenu();
        try
        {
            for (var index = 0; index < entries.Count; index++)
            {
                InsertEntry(menu, entries[index], (uint)index);
            }

            // The menu closes when the user clicks elsewhere only if its owner is the foreground window.
            TrayNative.SetForegroundWindow(_window);
            var chosen = TrayNative.TrackPopupMenuEx(
                menu, TrayNative.TpmReturnCommand | TrayNative.TpmRightButton | TrayNative.TpmBottomAlign | TrayNative.TpmNoNotify, x, y, _window, nint.Zero);
            TrayNative.PostMessage(_window, TrayNative.WmNull, 0, 0);
            if (chosen != NoCommandChosen)
            {
                EntryChosen?.Invoke(entries[(int)chosen - 1]);
            }
        }
        finally
        {
            TrayNative.DestroyMenu(menu);
        }
    }

    /// <summary>Adds an entry. A row of a profile has its state in a second column, which a native menu draws at the right as it does a shortcut.</summary>
    private static void InsertEntry(nint menu, TrayEntry entry, uint position)
    {
        var text = entry.Detail is { } detail ? entry.Text + ColumnSeparator + detail : entry.Text;
        var info = new TrayNative.MenuItemInfo { Size = (uint)sizeof(TrayNative.MenuItemInfo) };
        if (entry.IsSeparator)
        {
            info.Mask = TrayNative.MiimFType;
            info.Type = TrayNative.MftSeparator;
            TrayNative.InsertMenuItem(menu, position, byPosition: true, ref info);
            return;
        }

        info.Mask = TrayNative.MiimFType | TrayNative.MiimId | TrayNative.MiimString | TrayNative.MiimState;
        info.Type = TrayNative.MftString | (entry.Mark == MenuMark.Mixed ? TrayNative.MftRadioCheck : 0);
        info.State = (entry.IsEnabled ? 0 : TrayNative.MfsGrayed)
            | (entry.Mark == MenuMark.Off ? 0 : TrayNative.MfsChecked)
            | (entry.IsDefault ? TrayNative.MfsDefault : 0);
        info.Id = position + 1;
        fixed (char* start = text)
        {
            info.TypeData = (nint)start;
            info.Cch = (uint)text.Length;
            TrayNative.InsertMenuItem(menu, position, byPosition: true, ref info);
        }
    }
}
