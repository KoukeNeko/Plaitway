using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>The sidebar, the routing of the window and what it shows when the helper is not ready.</summary>
public sealed class ShellViewModelTests(DaemonBinary binary)
{
    private static ShellViewModel NewShell(AppHarness app, FakeDialogs? dialogs = null, FakeFilePicker? picker = null) =>
        new(app.Model, new FakeClipboard(), picker ?? new FakeFilePicker(), dialogs ?? new FakeDialogs());

    [Fact]
    public async Task TheSidebarListsTheProfilesInPriorityOrderAndEditsItsRowsInPlace()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var sidebar = new SidebarViewModel(app.Model);
            var office = await app.ImportFixtureAsync("office.ovpn");
            var lab = await app.ImportFixtureAsync("lab.ovpn");
            var home = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());

            Assert.Equal([office, lab, home], sidebar.Rows.Select(row => row.Id));
            Assert.Equal(["office", "lab", "home"], sidebar.Rows.Select(row => row.Name));
            Assert.Equal("OpenVPN \u00B7 Disconnected", sidebar.Rows[0].Subtitle);
            Assert.Equal("WireGuard \u00B7 Disconnected", sidebar.Rows[2].Subtitle);
            Assert.Equal("home, WireGuard, Disconnected", sidebar.Rows[2].AutomationName);

            // A change of one profile does not make a new row: the selection, the focus and a drag survive it.
            var row = sidebar.Rows[0];
            await app.Store.SetEnabledAsync(office, enabled: true);
            await app.WaitForStateAsync(office, ProfileState.Connected);
            Assert.Same(row, sidebar.Rows[0]);
            Assert.Equal("Connected", row.StateLabel);
            Assert.Equal("Disconnect", row.ToggleLabel);

            // Nor does a reorder: the rows move.
            var rows = sidebar.Rows.ToList();
            await app.Model.MoveAsync([2], 0);
            await Wait.UntilAsync("home first", () => sidebar.Rows[0].Id == home);
            Assert.Equal([home, office, lab], sidebar.Rows.Select(candidate => candidate.Id));
            Assert.All(sidebar.Rows, candidate => Assert.Contains(candidate, rows));
        });
    }

    [Fact]
    public async Task ADragInTheListIsSentAsTheNewOrder()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var sidebar = new SidebarViewModel(app.Model);
            var a = await app.ImportFixtureAsync("a.ovpn");
            var b = await app.ImportFixtureAsync("b.ovpn");

            // What the list does when the user drops the second row on the first.
            sidebar.Rows.Move(1, 0);
            await sidebar.ApplyDraggedOrderCommand.ExecuteAsync(null);

            await Wait.UntilAsync("b first", () => app.Store.Profiles.Select(profile => profile.Id).SequenceEqual([b, a]));
            Assert.Equal([b, a], sidebar.Rows.Select(row => row.Id));
        });
    }

    [Fact]
    public async Task TheSidebarSelectionIsTheModelsSelection()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var sidebar = new SidebarViewModel(app.Model);
            var id = await app.ImportFixtureAsync("office.ovpn");
            var changed = new List<string?>();
            sidebar.PropertyChanged += (_, args) => changed.Add(args.PropertyName);

            sidebar.SelectDiagnostics();
            Assert.True(sidebar.IsDiagnosticsSelected);
            Assert.Null(sidebar.SelectedProfileId);

            sidebar.SelectSettings();
            Assert.True(sidebar.IsSettingsSelected);

            sidebar.SelectProfile(id);
            Assert.Equal(id, sidebar.SelectedProfileId);
            Assert.Contains(nameof(SidebarViewModel.Selection), changed);
        });
    }

    [Fact]
    public async Task TheWindowShowsThePageOfWhatIsSelected()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            Assert.IsType<EmptyProfilesViewModel>(shell.Page);

            var office = await app.ImportFixtureAsync("office.ovpn");
            var lab = await app.ImportFixtureAsync("lab.ovpn");
            var first = Assert.IsType<ProfilePageViewModel>(shell.Page);
            Assert.Equal(office, first.ProfileId);
            Assert.Equal("office", first.Title);
            Assert.Equal("OpenVPN \u00B7 Disconnected", first.Subtitle);

            shell.Sidebar.SelectProfile(lab);
            var second = Assert.IsType<ProfilePageViewModel>(shell.Page);
            Assert.NotSame(first, second);
            Assert.Equal("lab", second.Title);

            shell.ShowDiagnostics();
            Assert.IsType<DiagnosticsViewModel>(shell.Page);

            shell.ShowSettings();
            Assert.IsType<AppSettingsViewModel>(shell.Page);

            // The profile that is open goes away: the first one that is left takes its place.
            shell.Sidebar.SelectProfile(lab);
            await app.Model.DeleteAsync(lab);
            await Wait.UntilAsync("the profile to go", () => app.Store.Find(lab) is null);
            Assert.Equal(office, Assert.IsType<ProfilePageViewModel>(shell.Page).ProfileId);
        });
    }

    [Fact]
    public async Task ThePageOfAWireGuardProfileSaysWireGuardAndShowsItsOwnEndpoint()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            await app.ImportFixtureAsync("office.ovpn");
            var home = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());

            shell.Sidebar.SelectProfile(home);

            var page = Assert.IsType<ProfilePageViewModel>(shell.Page);
            Assert.Equal(home, page.ProfileId);
            Assert.Equal("WireGuard · Disconnected", page.Subtitle);
            Assert.Contains(page.Overview.Details, row => row.Value == "203.0.113.5:51820");

            await app.Store.SetEnabledAsync(home, enabled: true);
            await app.WaitForStateAsync(home, ProfileState.Connected);
            Assert.Equal("WireGuard · Connected", page.Subtitle);
            Assert.Contains(page.Overview.Details, row => row.Label == "Remote" && row.Value == "203.0.113.5:51820");
        });
    }

    [Fact]
    public async Task TheProfilePageHasFiveTabsAndKeepsTheOneTheUserChose()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var office = await app.ImportFixtureAsync("office.ovpn");
            var lab = await app.ImportFixtureAsync("lab.ovpn");
            var page = Assert.IsType<ProfilePageViewModel>(shell.Page);

            Assert.Equal(["Overview", "Routes and DNS", "Logs", "Configuration", "Settings"], page.Tabs.Select(tab => tab.Label));
            Assert.IsType<OverviewViewModel>(page.CurrentTab);

            page.SelectedTab = page.Tabs[(int)ProfileSection.Routes];
            Assert.IsType<RoutesViewModel>(page.CurrentTab);
            Assert.Equal(ProfileSection.Routes, app.Model.ProfileSection);

            // The page of a profile stays when another profile is selected.
            shell.Sidebar.SelectProfile(lab);
            var other = Assert.IsType<ProfilePageViewModel>(shell.Page);
            Assert.IsType<RoutesViewModel>(other.CurrentTab);
            Assert.Equal(lab, ((RoutesViewModel)other.CurrentTab!).ProfileId);
            Assert.NotEqual(office, lab);

            shell.ShowSection(3);
            Assert.IsType<ConfigurationViewModel>(other.CurrentTab);
            shell.ShowSection(4);
            Assert.IsType<ProfileSettingsViewModel>(other.CurrentTab);
        });
    }

    [Fact]
    public async Task TheBarAtTheBottomOfAProfileConnectsDisconnectsAndRetries()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var broken = await app.ImportFixtureAsync("broken.ovpn", Fixture.OpenVpn(markers: ["# fake: fail"]));
            var page = Assert.IsType<ProfilePageViewModel>(shell.Page);
            Assert.Equal("Connect", page.ToggleLabel);
            Assert.True(page.ToggleIsProminent);
            Assert.False(page.ShowsRetry);

            await page.ToggleCommand.ExecuteAsync(null);
            await app.WaitForStateAsync(broken, ProfileState.Failed);

            // A failed profile is switched on already: the bar offers to disconnect it and to try again.
            Assert.Equal("Disconnect", page.ToggleLabel);
            Assert.False(page.ToggleIsProminent);
            Assert.True(page.ShowsRetry);

            await page.ToggleCommand.ExecuteAsync(null);
            await app.WaitForStateAsync(broken, ProfileState.Disconnected);
            Assert.False(page.ShowsRetry);
        });
    }

    [Fact]
    public async Task AHelperOfAnotherVersionIsCalledOutOfDateAboveTheProfiles()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { AppVersion = "9.9.9", HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            await Wait.UntilAsync("the daemon's version", () => app.Store.DaemonInfo is not null);

            var banner = Assert.IsType<NoticeBanner>(shell.Banner);
            Assert.Equal("Helper out of date", banner.Title);
            Assert.Equal($"{app.Store.DaemonInfo!.Version} \u2192 9.9.9", banner.Detail);
            Assert.Equal("Reinstall Helper", banner.ActionLabel);
            Assert.Equal(SetupAction.ReinstallHelper, banner.Action);
            Assert.True(shell.ShowsProfiles);

            // Reinstalling drops every tunnel, so the button asks first.
            await shell.PerformBannerActionCommand.ExecuteAsync(null);
            Assert.True(app.Model.IsConfirmingReinstall);
        });
    }

    [Fact]
    public async Task AHelperThatSomeoneStartedByHandIsNotOfferedToBeReinstalled()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { AppVersion = "9.9.9", HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            await Wait.UntilAsync("the daemon's version", () => app.Store.DaemonInfo is not null);

            var banner = Assert.IsType<NoticeBanner>(shell.Banner);
            Assert.Null(banner.Action);
            Assert.Equal(app.Text.StartTheHelperAgainFromThisVersion, banner.Hint);
        });
    }

    [Fact]
    public async Task AHelperThatStopsAnsweringKeepsTheProfilesInViewWithANote()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            await app.ImportFixtureAsync("office.ovpn");

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == DaemonConnection.Unavailable);

            Assert.True(shell.ShowsProfiles);
            Assert.False(shell.ShowsSetup);
            var banner = Assert.IsType<NoticeBanner>(shell.Banner);
            Assert.Equal("Helper unavailable", banner.Title);
            Assert.Equal(SetupAction.Retry, banner.Action);
        });
    }

    [Fact]
    public async Task WithNoProfilesToKeepTheSetupPageTakesTheWindow()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Stopped });
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var shown = 0;
            shell.ShowWindowRequested += () => shown++;

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == DaemonConnection.Unavailable);

            Assert.True(shell.ShowsSetup);
            Assert.Equal("Helper stopped", shell.Setup.Title);
            Assert.Equal("Start Helper", shell.Setup.PrimaryLabel);
            Assert.True(shell.Setup.HasPrimary);
            Assert.False(shell.Setup.HasSecondary);
            Assert.Equal(1, shown);

            app.Launcher.Effect = _ => app.Service.State = HelperState.Running;
            await shell.Setup.PrimaryCommand.ExecuteAsync(null);
            Assert.Equal(["C:\\Plaitway\\app\\plaitwayd.exe start"], app.Launcher.Calls);
            Assert.Equal("Connecting", shell.Setup.Title);
            Assert.True(shell.Setup.ShowsProgress);
        });
    }

    [Fact]
    public async Task AProfileThatAsksForCredentialsBringsTheWindowForward()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var shown = 0;
            shell.ShowWindowRequested += () => shown++;
            var id = await app.ImportFixtureAsync("router.ovpn", Fixture.OpenVpn(markers: ["# fake: needs-credentials"]));

            await app.Store.SetEnabledAsync(id, enabled: true);

            await Wait.UntilAsync("the prompt", () => app.Store.CredentialPrompts.Count > 0);
            Assert.True(shown > 0);
        });
    }

    [Fact]
    public async Task ImportingAsksForFilesAndSelectsTheLastOne()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["a.ovpn"] = "client\nremote vpn.example.net 1194\n", ["b.conf"] = Fixture.WireGuardText() });
        await app.RunAsync(async () =>
        {
            var picker = new FakeFilePicker { Chosen = [directory.Resolve("a.ovpn"), directory.Resolve("b.conf")] };
            using var shell = NewShell(app, picker: picker);

            await shell.ImportCommand.ExecuteAsync(null);

            Assert.Equal(1, picker.Asked);
            Assert.Equal(["a", "b"], app.Store.Profiles.Select(profile => profile.Name));
            Assert.Equal(app.Store.Profiles[1].Id, Assert.IsType<ProfilePageViewModel>(shell.Page).ProfileId);
        });
    }

    [Fact]
    public async Task ImportingDoesNothingWhileTheHelperDoesNotAnswer()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var picker = new FakeFilePicker();
            using var shell = NewShell(app, picker: picker);
            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == DaemonConnection.Unavailable);

            await shell.ImportCommand.ExecuteAsync(null);

            Assert.Equal(0, picker.Asked);
        });
    }

    [Fact]
    public async Task TheShortcutsOfTheMacMenuActOnTheSelectedProfile()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var a = await app.ImportFixtureAsync("a.ovpn");
            var b = await app.ImportFixtureAsync("b.ovpn");
            shell.Sidebar.SelectProfile(b);

            await shell.ToggleSelectedProfileAsync();
            await app.WaitForStateAsync(b, ProfileState.Connected);

            await shell.MoveSelectedProfileAsync(-1);
            await Wait.UntilAsync("b first", () => app.Store.Profiles.Select(profile => profile.Id).SequenceEqual([b, a]));

            shell.DeleteSelectedProfile();
            Assert.Equal(b, app.Model.PendingDeletion);

            shell.ShowSection(2);
            Assert.Equal(ProfileSection.Logs, app.Model.ProfileSection);
            var searches = app.Model.SearchRequest;
            shell.FindInPage();
            Assert.Equal(searches + 1, app.Model.SearchRequest);

            shell.ShowSection(0);
            shell.FindInPage();
            Assert.Equal(searches + 1, app.Model.SearchRequest);
        });
    }
}
