using System.Windows.Automation;
using Plaitway.App.UiTests.Support;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

[assembly: AssemblyFixture(typeof(DaemonBinary))]

namespace Plaitway.App.UiTests;

/// <summary>
/// The app as a person meets it: started against a daemon of its own (<c>plaitwayd -fake</c> on a pipe of its own), seen
/// through UI Automation, and ended through its tray icon. The tests drive real windows on the screen of whoever runs them,
/// so they stay in one class: the tests of a class do not run at the same time. Nothing here touches the real service, a route
/// or a startup entry.
/// The names are those of the English UI.
/// </summary>
[Trait("Category", "Ui")]
public sealed class AppWindowTests(DaemonBinary binary)
{
    private const string QuitMenuEntry = "Quit Plaitway";
    private const string QuitQuestion = "Quit Plaitway?";
    private const string QuitButton = "Quit";
    private const string DisconnectAllAndQuitButton = "Disconnect All and Quit";
    private const string CancelButton = "Cancel";
    private const byte VkF6 = 0x75;
    private const string ImportButton = "Import Profile…";
    private const string LanguageBox = "Language";
    private const string RestartButton = "Restart Plaitway";
    private const string TraditionalChineseName = "繁體中文";
    private const string SettingsInChinese = "設定";
    private const int QuitAttempts = 3;
    private static readonly TimeSpan KeyboardGrace = TimeSpan.FromMilliseconds(500);
    private static readonly TimeSpan ExitTimeout = TimeSpan.FromSeconds(20);
    private static readonly TimeSpan QuestionTimeout = TimeSpan.FromSeconds(6);

    private static readonly string[] ProfileTabs = ["Overview", "Routes and DNS", "Logs", "Configuration", "Settings"];

    [Fact]
    public async Task EveryControlThatCanBeActedOnHasAName()
    {
        await using var world = await World.StartAsync(binary, World.Office, World.Home);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        var window = Uia.Window(app.WindowHandle);
        await WaitForProfilesAsync(window);
        var problems = new List<string>();

        void Check(string page) => problems.AddRange(Uia.ControlsWithoutAName(window).Select(control => $"{page}: {control}"));

        foreach (var profile in new[] { World.Office, World.Home, World.Lab })
        {
            Uia.Select(Uia.FindByPrefix(window, profile + ", ", ControlType.ListItem) ?? throw new InvalidOperationException($"no {profile} in the sidebar"));
            foreach (var tab in ProfileTabs)
            {
                await SelectAsync(window, tab, ControlType.TabItem);
                Check($"{profile} / {tab}");
            }
        }

        await SelectAsync(window, "Diagnostics", ControlType.ListItem);
        Check("Diagnostics");
        await SelectAsync(window, "Helper log", ControlType.TabItem);
        Check("Diagnostics / Helper log");
        await SelectAsync(window, "Settings", ControlType.ListItem);
        Check("Settings");

        Assert.Empty(problems);
    }

    [Fact]
    public async Task ALanguageChosenInTheSettingsIsLoadedWhenTheAppRestartsItself()
    {
        await using var world = await World.StartAsync(binary);
        await using var app = await AppProcess.StartAsync(world.Pipe, language: null, savedLanguage: "en-US");
        var window = Uia.Window(app.WindowHandle);
        await WaitForProfilesAsync(window);
        await SelectAsync(window, "Settings", ControlType.ListItem);
        await Wait.UntilAsync("the language box", () => Uia.Find(window, LanguageBox, ControlType.ComboBox) is not null);
        Assert.Null(Uia.Find(window, RestartButton, ControlType.Button));

        Uia.Choose(Uia.Find(window, LanguageBox, ControlType.ComboBox)!, TraditionalChineseName);

        await Wait.UntilAsync("the restart button", () => Uia.Find(window, RestartButton, ControlType.Button) is not null);
        Uia.Invoke(Uia.Find(window, RestartButton, ControlType.Button)!);
        await Wait.UntilAsync("the first copy to end", () => app.Process.HasExited, ExitTimeout);

        await using var restarted = await app.WaitForRestartedCopyAsync();
        var restartedWindow = Uia.Window(restarted.WindowHandle);
        await Wait.UntilAsync("the sidebar in Traditional Chinese", () => Uia.Find(restartedWindow, SettingsInChinese, ControlType.ListItem) is not null);
    }

    [Fact]
    public async Task ClosingTheWindowHidesItAndTheIconBringsItBack()
    {
        await using var world = await World.StartAsync(binary);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        await WaitForProfilesAsync(Uia.Window(app.WindowHandle));

        Close(app);
        await Wait.UntilAsync("the window to hide", () => !Native.IsWindowVisible(app.WindowHandle));
        Assert.False(app.Process.HasExited, "closing the window must leave the app in the tray");

        app.ClickTrayIcon();
        await Wait.UntilAsync("the window to come back", () => Native.IsWindowVisible(app.WindowHandle));
    }

    [Fact]
    public async Task ASecondStartBringsTheFirstWindowForwardAndEnds()
    {
        await using var world = await World.StartAsync(binary);
        await using var first = await AppProcess.StartAsync(world.Pipe);
        await WaitForProfilesAsync(Uia.Window(first.WindowHandle));
        Close(first);
        await Wait.UntilAsync("the window to hide", () => !Native.IsWindowVisible(first.WindowHandle));

        await using var second = AppProcess.StartSecondCopy(world.Pipe);

        await Wait.UntilAsync("the second copy to end", () => second.Process.HasExited, ExitTimeout);
        Assert.Equal(0, second.Process.ExitCode);
        await Wait.UntilAsync("the first window to come forward", () => Native.IsWindowVisible(first.WindowHandle));
        Assert.False(first.Process.HasExited);
    }

    [Fact]
    public async Task QuitInTheTrayMenuEndsTheAppWhenNothingIsOn()
    {
        await using var world = await World.StartAsync(binary);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        await WaitForProfilesAsync(Uia.Window(app.WindowHandle));

        await QuitFromTrayUntilAsync(app, () => app.Process.HasExited);

        Assert.True(app.Process.HasExited);
    }

    [Fact]
    public async Task QuitWithAProfileOnAsksFirstAndCancelKeepsTheApp()
    {
        await using var world = await World.StartAsync(binary, World.Office);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        var window = Uia.Window(app.WindowHandle);
        await WaitForProfilesAsync(window);

        await QuitFromTrayUntilAsync(app, () => Uia.Find(window, QuitQuestion, ControlType.Text) is not null);
        Uia.Invoke(Uia.Find(window, CancelButton, ControlType.Button)!);

        await Wait.UntilAsync("the question to close", () => Uia.Find(window, QuitQuestion, ControlType.Text) is null);
        Assert.False(app.Process.HasExited);
    }

    [Fact]
    public async Task QuitLeavesAConnectedProfileConnected()
    {
        await using var world = await World.StartAsync(binary, World.Office);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        var window = Uia.Window(app.WindowHandle);
        await WaitForProfilesAsync(window);

        await QuitFromTrayUntilAsync(app, () => Uia.Find(window, QuitButton, ControlType.Button) is not null);
        Uia.Invoke(Uia.Find(window, QuitButton, ControlType.Button)!);

        await Wait.UntilAsync("the app to end", () => app.Process.HasExited, ExitTimeout);
        Assert.Equal(ProfileState.Connected, (await world.ProfileAsync(World.Office)).State);
    }

    [Fact]
    public async Task DisconnectAllAndQuitDisconnectsBeforeTheAppEnds()
    {
        await using var world = await World.StartAsync(binary, World.Office, World.Home);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        var window = Uia.Window(app.WindowHandle);
        await WaitForProfilesAsync(window);

        await QuitFromTrayUntilAsync(app, () => Uia.Find(window, DisconnectAllAndQuitButton, ControlType.Button) is not null);
        Uia.Invoke(Uia.Find(window, DisconnectAllAndQuitButton, ControlType.Button)!);

        await Wait.UntilAsync("the app to end", () => app.Process.HasExited, ExitTimeout);
        foreach (var name in new[] { World.Office, World.Home })
        {
            Assert.False((await world.ProfileAsync(name)).DesiredEnabled, $"{name} was left on");
        }
    }

    [Fact]
    public async Task F6MovesTheFocusBetweenTheSidebarAndThePage()
    {
        await using var world = await World.StartAsync(binary, World.Office);
        await using var app = await AppProcess.StartAsync(world.Pipe);
        var window = Uia.Window(app.WindowHandle);
        await WaitForProfilesAsync(window);
        Native.Activate(app.WindowHandle);
        await Task.Delay(KeyboardGrace);
        Assert.SkipUnless(Native.HasKeyboard(app.Process.Id), "Windows did not give the app the keyboard (an app that is started in the background may not take it), and a key must not go to another program");
        Uia.FindByPrefix(window, World.Office + ", ", ControlType.ListItem)!.SetFocus();
        await Wait.UntilAsync("the focus to be in the sidebar", () => IsInSidebar(Uia.FocusedControl(window)));

        Assert.True(Native.PressKey(app.Process.Id, VkF6));
        await Wait.UntilAsync("the focus to reach the page", () => IsInPage(Uia.FocusedControl(window)));

        Assert.True(Native.PressKey(app.Process.Id, VkF6));
        await Wait.UntilAsync("the focus to return to the sidebar", () => IsInSidebar(Uia.FocusedControl(window)));
    }

    private static Task WaitForProfilesAsync(AutomationElement window) =>
        Wait.UntilAsync("the profiles in the sidebar", () => Uia.FindByPrefix(window, World.Lab + ", ", ControlType.ListItem) is not null);

    private static async Task SelectAsync(AutomationElement window, string name, ControlType type)
    {
        await Wait.UntilAsync($"'{name}'", () => Uia.Find(window, name, type) is not null);
        Uia.Select(Uia.Find(window, name, type)!);
    }

    // The sidebar is its two lists and the button above them; F6 may land on any of them.
    private static bool IsInSidebar(AutomationElement? element) =>
        element is not null
        && (element.Current is { ControlType: var type, Name: ImportButton } && type == ControlType.Button
            || HasAncestor(element, ancestor => ancestor.Current.ControlType == ControlType.List));

    private static bool IsInPage(AutomationElement? element) => element is not null && HasAncestor(element, ancestor => ancestor.Current.ControlType == ControlType.Tab);

    private static bool HasAncestor(AutomationElement element, Func<AutomationElement, bool> matches)
    {
        var walker = TreeWalker.ControlViewWalker;
        for (var current = walker.GetParent(element); current is not null; current = walker.GetParent(current))
        {
            if (matches(current))
            {
                return true;
            }
        }

        return false;
    }

    private static void Close(AppProcess app) =>
        ((WindowPattern)Uia.Window(app.WindowHandle).GetCurrentPattern(WindowPattern.Pattern)).Close();

    /// <summary>
    /// Chooses Quit in the tray menu until what it leads to happens (the question appears, or the app ends). A menu of Windows takes the choice only while its owner is
    /// the foreground window, which an app that a test started in the background is not always allowed to be, so a choice that
    /// was dropped is made again.
    /// </summary>
    private static async Task QuitFromTrayUntilAsync(AppProcess app, Func<bool> outcomeIsShown)
    {
        for (var attempt = 1; attempt <= QuitAttempts; attempt++)
        {
            await ChooseInTrayMenuAsync(app, QuitMenuEntry);
            try
            {
                await Wait.UntilAsync("what quitting leads to", outcomeIsShown, QuestionTimeout);
                return;
            }
            catch (TimeoutException) when (attempt < QuitAttempts)
            {
                // The menu may still be open, or already closed; the next attempt takes either.
            }
        }
    }

    /// <summary>
    /// Chooses an entry of the tray menu through UI Automation. A menu of Windows closes by itself when its owner loses the
    /// active window, which any other program on the desktop can cause, and Windows drops some choices made this way, so a
    /// caller that expects a result looks for it and chooses again (see <see cref="QuitFromTrayUntilAsync"/>).
    /// </summary>
    private static async Task ChooseInTrayMenuAsync(AppProcess app, string entry)
    {
        if (!Uia.IsAnyMenuOpen())
        {
            app.OpenTrayMenu();
        }

        AutomationElement? item = null;
        await Wait.UntilAsync($"'{entry}' in the tray menu", () => (item = Uia.FindTrayMenuItem(entry)) is not null);
        Uia.Invoke(item!);
    }
}