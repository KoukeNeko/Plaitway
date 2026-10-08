using System.Runtime.InteropServices;
using System.Windows.Automation;

namespace Plaitway.App.UiTests.Support;

/// <summary>The window as UI Automation shows it, which is what a screen reader walks.</summary>
internal static class Uia
{
    private const string NativeMenuClass = "#32768";

    /// <summary>What the user can act on or move through, and so what a screen reader has to be able to name.</summary>
    private static readonly ControlType[] TypesThatNeedAName =
    [
        ControlType.Button, ControlType.CheckBox, ControlType.ComboBox, ControlType.Edit, ControlType.Hyperlink, ControlType.List,
        ControlType.ListItem, ControlType.MenuItem, ControlType.RadioButton, ControlType.Slider, ControlType.SplitButton, ControlType.Tab,
        ControlType.TabItem, ControlType.Document, ControlType.Image, ControlType.ProgressBar, ControlType.DataItem, ControlType.TreeItem,
        ControlType.Tree, ControlType.Table,
    ];

    /// <summary>The window of a process, from its handle.</summary>
    public static AutomationElement Window(nint handle) => AutomationElement.FromHandle(handle);

    /// <summary>The first control with this name and type below <paramref name="root"/>, or null.</summary>
    public static AutomationElement? Find(AutomationElement root, string name, ControlType type) =>
        root.FindFirst(TreeScope.Descendants, new AndCondition(
            new PropertyCondition(AutomationElement.NameProperty, name),
            new PropertyCondition(AutomationElement.ControlTypeProperty, type)));

    /// <summary>The first control of this type whose name starts with <paramref name="prefix"/>, or null.</summary>
    public static AutomationElement? FindByPrefix(AutomationElement root, string prefix, ControlType type) =>
        root.FindAll(TreeScope.Descendants, new PropertyCondition(AutomationElement.ControlTypeProperty, type))
            .Cast<AutomationElement>()
            .FirstOrDefault(element => element.Current.Name.StartsWith(prefix, StringComparison.Ordinal));

    /// <summary>Chooses a list item or a tab, as an arrow key and a space would.</summary>
    public static void Select(AutomationElement element) =>
        ((SelectionItemPattern)element.GetCurrentPattern(SelectionItemPattern.Pattern)).Select();

    /// <summary>Presses a button or a menu item.</summary>
    public static void Invoke(AutomationElement element) =>
        ((InvokePattern)element.GetCurrentPattern(InvokePattern.Pattern)).Invoke();

    /// <summary>
    /// The control that has the keyboard focus in the window. <c>AutomationElement.FocusedElement</c> answers with the pane that
    /// hosts the XAML content until the app's own focus has been reported, so the tree is asked instead; the panes that
    /// host the content are never the answer.
    /// </summary>
    public static AutomationElement? FocusedControl(AutomationElement window) =>
        window.FindAll(TreeScope.Descendants, new PropertyCondition(AutomationElement.HasKeyboardFocusProperty, true))
            .Cast<AutomationElement>()
            .FirstOrDefault(element => element.Current.ControlType != ControlType.Pane);

    /// <summary>The controls under <paramref name="root"/> that a screen reader would read as "button" and nothing more.</summary>
    public static IReadOnlyList<string> ControlsWithoutAName(AutomationElement root)
    {
        var found = new List<string>();
        Collect(root, string.Empty, found);
        return found;
    }

    /// <summary>
    /// An entry of the menu that the notification-area icon opened. The menu is a window of Windows itself, a child of the
    /// desktop, and the desktop may have other menus open (or leftovers of them), so every one of them is looked in.
    /// </summary>
    public static AutomationElement? FindTrayMenuItem(string name)
    {
        var menus = AutomationElement.RootElement.FindAll(TreeScope.Children, new PropertyCondition(AutomationElement.ClassNameProperty, NativeMenuClass));
        return menus.Cast<AutomationElement>()
            .Select(menu => menu.FindFirst(TreeScope.Descendants, new AndCondition(
                new PropertyCondition(AutomationElement.NameProperty, name),
                new PropertyCondition(AutomationElement.ControlTypeProperty, ControlType.MenuItem))))
            .FirstOrDefault(item => item is not null);
    }

    /// <summary>Whether a menu of Windows is open on the desktop.</summary>
    public static bool IsAnyMenuOpen() =>
        AutomationElement.RootElement.FindFirst(TreeScope.Children, new PropertyCondition(AutomationElement.ClassNameProperty, NativeMenuClass)) is not null;
    private static void Collect(AutomationElement element, string path, List<string> found)
    {
        var type = element.Current.ControlType;
        var here = path.Length == 0 ? type.ProgrammaticName : $"{path} > {type.ProgrammaticName}";
        if (TypesThatNeedAName.Contains(type) && string.IsNullOrWhiteSpace(element.Current.Name))
        {
            found.Add($"{here} (class {element.Current.ClassName})");
        }

        var walker = TreeWalker.ControlViewWalker;
        for (var child = walker.GetFirstChild(element); child is not null; child = walker.GetNextSibling(child))
        {
            Collect(child, here, found);
        }
    }
}

/// <summary>The few calls to Windows that UI Automation does not cover.</summary>
internal static partial class Native
{
    private const string User32 = "user32.dll";
    private const uint KeyUp = 0x2;

    [LibraryImport(User32)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool IsWindowVisible(nint window);

    [LibraryImport(User32)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool SetForegroundWindow(nint window);

    [LibraryImport(User32)]
    private static partial nint GetForegroundWindow();

    [LibraryImport(User32)]
    private static partial void SwitchToThisWindow(nint window, [MarshalAs(UnmanagedType.Bool)] bool altTab);

    /// <summary>Asks Windows to make the window the active one; it may decline, which <see cref="HasKeyboard"/> tells.</summary>
    public static void Activate(nint window) => SwitchToThisWindow(window, altTab: true);

    [LibraryImport(User32)]
    private static partial uint GetWindowThreadProcessId(nint window, out uint processId);

    [LibraryImport(User32, EntryPoint = "keybd_event")]
    private static partial void SendKeyEvent(byte virtualKey, byte scanCode, uint flags, nuint extraInfo);

    /// <summary>The window that has the keyboard belongs to this process.</summary>
    public static bool HasKeyboard(int processId)
    {
        _ = GetWindowThreadProcessId(GetForegroundWindow(), out var owner);
        return owner == (uint)processId;
    }

    /// <summary>
    /// Presses a key, but only while a window of the process has the keyboard: a key that goes to another program is a key in
    /// the wrong place. Returns whether it was sent.
    /// </summary>
    public static bool PressKey(int processId, byte virtualKey)
    {
        if (!HasKeyboard(processId))
        {
            return false;
        }

        SendKeyEvent(virtualKey, 0, 0, 0);
        SendKeyEvent(virtualKey, 0, KeyUp, 0);
        return true;
    }

    [LibraryImport(User32, EntryPoint = "PostMessageW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool PostMessage(nint window, uint message, nuint wordParameter, nint longParameter);
}
