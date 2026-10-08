using Grpc.Core;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Logs;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Pages;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client.Import;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/App/AppModelTests.swift, except the import cases (AppModelImportTests), against the real daemon.</summary>
public sealed class AppModelTests(DaemonBinary binary)
{
    // delete, reorder, edit

    [Fact]
    public async Task DeletionAsksFirstAndThenSelectsAnotherProfile()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var a = await app.ImportFixtureAsync("a.ovpn");
            var b = await app.ImportFixtureAsync("b.ovpn");
            app.Model.Selection = SidebarItem.Profile(b);

            app.Model.RequestDeletion(b);
            Assert.Equal(b, app.Model.PendingDeletion);
            await Task.Delay(200);
            Assert.Equal(2, app.Store.Profiles.Count);

            await app.Model.DeleteAsync(b);
            await Wait.UntilAsync("the profile to go", () => app.Store.Find(b) is null);
            app.Model.ReconcileSelection();
            Assert.Equal(SidebarItem.Profile(a), app.Model.Selection);

            // The last one: nothing is left to select.
            app.Model.RequestDeletion(a);
            await app.Model.DeleteAsync(a);
            await Wait.UntilAsync("the last profile to go", () => app.Store.Profiles.Count == 0);
            app.Model.ReconcileSelection();
            Assert.Null(app.Model.Selection);
        });
    }

    [Fact]
    public async Task ReconcilingKeepsASelectionThatStillExists()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var a = await app.ImportFixtureAsync("a.ovpn");
            await app.ImportFixtureAsync("b.ovpn");
            app.Model.Selection = SidebarItem.Profile(a);
            app.Model.ReconcileSelection();
            Assert.Equal(SidebarItem.Profile(a), app.Model.Selection);

            app.Model.Selection = SidebarItem.Diagnostics;
            app.Model.ReconcileSelection();
            Assert.Equal(SidebarItem.Diagnostics, app.Model.Selection);

            app.Model.Selection = null;
            app.Model.ReconcileSelection();
            Assert.Equal(SidebarItem.Profile(a), app.Model.Selection);
        });
    }

    [Fact]
    public async Task DraggingAProfileSetsThePriorityOrder()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var a = await app.ImportFixtureAsync("a.ovpn");
            var b = await app.ImportFixtureAsync("b.ovpn");
            var c = await app.ImportFixtureAsync("c.ovpn");

            await app.Model.MoveAsync([2], 0);

            // The daemon reports the new priorities one profile at a time; the store settles on the last.
            await Wait.UntilAsync(
                "c first",
                () => app.Store.Profiles.Select(profile => profile.Id).SequenceEqual([c, a, b])
                    && app.Store.Profiles.Select(profile => profile.Settings.Priority).SequenceEqual([1, 2, 3]));

            await app.Model.MoveAsync(c, 1);
            await Wait.UntilAsync("c second", () => app.Store.Profiles.Select(profile => profile.Id).SequenceEqual([a, c, b]));

            // Past the end nothing happens, and nothing is reported.
            await app.Model.MoveAsync(b, 1);
            await app.Model.MoveAsync(a, -1);
            Assert.Null(app.Model.Alert);
            Assert.Equal([a, c, b], app.Store.Profiles.Select(profile => profile.Id));
        });
    }

    [Fact]
    public async Task RenamingTrimsAndIgnoresNothingNew()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");

            await app.Model.RenameAsync(id, "  Head Office \n");
            await Wait.UntilAsync("the rename", () => app.Store.Find(id)?.Name == "Head Office");

            await app.Model.RenameAsync(id, "   ");
            await app.Model.RenameAsync(id, "Head Office");
            await Task.Delay(150);
            Assert.Equal("Head Office", app.Store.Find(id)?.Name);
            Assert.Null(app.Model.Alert);
        });
    }

    [Fact]
    public async Task ChangingOneSettingKeepsTheOthers()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");

            await app.Model.ChangeSettingsAsync(id, settings => settings.AutoConnect = true);
            await Wait.UntilAsync("auto-connect", () => app.Store.Find(id)?.Settings.AutoConnect == true);
            await app.Model.ChangeSettingsAsync(id, settings => settings.TunnelMode = TunnelMode.Split);
            await Wait.UntilAsync("split tunnel", () => app.Store.Find(id)?.Settings.TunnelMode == TunnelMode.Split);

            Assert.True(app.Store.Find(id)?.Settings.AutoConnect);
            Assert.Equal(1, app.Store.Find(id)?.Settings.Priority);
        });
    }

    // failures

    [Fact]
    public async Task ACallThatFailsBecomesAnAlert()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            Assert.Null(app.Model.Alert);

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == Daemon.DaemonConnection.Unavailable);
            await app.Model.RenameAsync(id, "Other");

            var alert = Assert.IsType<AppAlert>(app.Model.Alert);
            Assert.Equal(AppAlert.TitleText(AlertTitle.SaveFailed, app.Text), alert.Title);
            Assert.Equal(app.Text.HelperUnavailable, alert.Message);
        });
    }

    [Fact]
    public void DaemonFailuresAreWorded()
    {
        var errors = new ErrorText(TestText.En);
        Assert.Equal("Administrator required", errors.UserMessage(new RpcException(new Status(StatusCode.PermissionDenied, "caller is not an administrator"))));
        Assert.Equal("line 3: nope", errors.UserMessage(new RpcException(new Status(StatusCode.InvalidArgument, "line 3: nope"))));
        Assert.Equal("Profile not found", errors.UserMessage(new RpcException(new Status(StatusCode.NotFound, "no such profile"))));
        Assert.Equal("Helper unavailable", errors.UserMessage(new RpcException(new Status(StatusCode.Unavailable, "gone"))));
        Assert.Contains("/x/ca.crt", errors.UserMessage(new ProfileImportException(new ProfileImportFailure.MissingFile("ca", "/x/ca.crt"))), StringComparison.Ordinal);

        // The file that was refused is named, so the user knows which line of the profile to look at.
        var outside = errors.UserMessage(new ProfileImportException(new ProfileImportFailure.OutsideProfileDirectory("key", "../id_ed25519")));
        Assert.Contains("../id_ed25519", outside, StringComparison.Ordinal);
        Assert.Contains("key", outside, StringComparison.Ordinal);
        Assert.Contains("/x/creds.txt", errors.UserMessage(new ProfileImportException(new ProfileImportFailure.NotCredentials("/x/creds.txt"))), StringComparison.Ordinal);
    }

    // helper

    [Fact]
    public async Task TracksTheHelperThroughItsStates()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { AppVersion = "0.1.0" });
        await app.RunAsync(async () =>
        {
            await Wait.UntilAsync("the daemon's version", () => app.Store.DaemonInfo is not null);
            Assert.Equal(DaemonSetup.VersionMismatch(app.Store.DaemonInfo!.Version, "0.1.0"), app.Model.Setup);

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == Daemon.DaemonConnection.Unavailable);
            Assert.Equal(DaemonSetup.NotResponding(), app.Model.Setup);

            // A retry waits for the daemon for a while before giving up on it.
            app.Model.Retry();
            Assert.Equal(DaemonSetup.Connecting, app.Model.Setup);

            app.Service.State = HelperState.NotInstalled;
            app.Installer.Refresh();
            Assert.Equal(SetupKind.NeedsInstall, app.Model.Setup.Kind);

            // The consent prompt is answered by the test: the service is there afterwards and has time to come up.
            app.Launcher.Effect = _ => app.Service.State = HelperState.Running;
            await app.Model.InstallHelperAsync();
            Assert.Equal(["C:\\Plaitway\\app\\plaitwayd.exe install -start"], app.Launcher.Calls);
            Assert.Equal(DaemonSetup.Connecting, app.Model.Setup);
            Assert.Null(app.Model.Alert);
        });
    }

    [Fact]
    public async Task AStoppedServiceOffersToStartIt()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Stopped });
        await app.RunAsync(async () =>
        {
            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == Daemon.DaemonConnection.Unavailable);
            Assert.Equal(SetupKind.Stopped, app.Model.Setup.Kind);
            Assert.True(app.Model.Setup.OffersInstallation);

            app.Launcher.Effect = _ => app.Service.State = HelperState.Running;
            await app.Model.PerformAsync(SetupAction.StartHelper);

            Assert.Equal(["C:\\Plaitway\\app\\plaitwayd.exe start"], app.Launcher.Calls);
            Assert.Equal(HelperState.Running, app.Installer.Status.State);
            Assert.Equal(DaemonSetup.Connecting, app.Model.Setup);
        });
    }

    [Fact]
    public async Task AFailedInstallIsReported()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            app.Launcher.Result = new ElevatedResult(ElevatedOutcome.Completed, 5);

            await app.Model.InstallHelperAsync();

            var alert = Assert.IsType<AppAlert>(app.Model.Alert);
            Assert.Equal(AppAlert.TitleText(AlertTitle.HelperFailed, app.Text), alert.Title);
            Assert.Contains(app.Text.HelperCommandFailedWithExitCode(5), alert.Message, StringComparison.Ordinal);
            Assert.Equal(HelperState.NotInstalled, app.Installer.Status.State);
            Assert.False(app.Model.IsSettling, "nothing was installed, so there is nothing to wait for");
        });
    }

    [Fact]
    public async Task ADeclinedConsentPromptIsADecisionAndNotAnError()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            app.Launcher.Result = new ElevatedResult(ElevatedOutcome.Declined, 0);

            await app.Model.InstallHelperAsync();

            Assert.Single(app.Launcher.Calls);
            Assert.Null(app.Model.Alert);
            Assert.False(app.Model.IsSettling);
        });
    }

    [Fact]
    public async Task UninstallingGoesThroughTheInstaller()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            app.Launcher.Effect = _ => app.Service.State = HelperState.NotInstalled;

            await app.Model.UninstallHelperAsync();

            Assert.Equal(["C:\\Plaitway\\app\\plaitwayd.exe uninstall"], app.Launcher.Calls);
            Assert.Equal(HelperState.NotInstalled, app.Installer.Status.State);
        });
    }

    [Fact]
    public async Task AFailedInstallPointsToTheCommandThatInstallsFromAnElevatedShell()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            app.Launcher.Failure = new System.ComponentModel.Win32Exception(2);
            await app.Model.InstallHelperAsync();
            Assert.Contains("plaitwayd.exe install -start", app.Model.Alert?.Message, StringComparison.Ordinal);
        });
    }

    [Fact]
    public async Task AHelperThatCannotBeFoundIsReportedAndNothingIsRun()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled, HasExecutable = false });
        await app.RunAsync(async () =>
        {
            await app.Model.InstallHelperAsync();

            Assert.Contains(app.Text.TheHelperFileIsMissingFromThisCopyOfPlaitwayInstallPlaitwayAgain, app.Model.Alert?.Message, StringComparison.Ordinal);
            Assert.Empty(app.Launcher.Calls);
        });
    }

    // a helper that something else started

    [Fact]
    public async Task ADaemonThatAnswersWithoutHavingBeenInstalledByTheAppIsLeftAlone()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled, AppVersion = "9.9.9" });
        await app.RunAsync(async () =>
        {
            await Wait.UntilAsync("the daemon's version", () => app.Store.DaemonInfo is not null);
            Assert.True(app.Model.IsHelperExternal);
            Assert.Equal(DaemonSetup.VersionMismatch(app.Store.DaemonInfo!.Version, "9.9.9"), app.Model.Setup);
            Assert.False(app.Model.Setup.OffersInstallation, "the window would open at every launch");

            // Registering again would put a second service next to the daemon that runs.
            await app.Model.ReinstallHelperAsync();
            Assert.Empty(app.Launcher.Calls);
            Assert.Null(app.Model.Alert);
        });
    }

    [Fact]
    public async Task AHelperTheAppInstalledIsNotExternalAndIsReinstalledAfterTheConfirmation()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            Assert.False(app.Model.IsHelperExternal);

            // Reinstalling drops every tunnel, so every way to it asks first.
            await app.Model.PerformAsync(SetupAction.ReinstallHelper);
            Assert.True(app.Model.IsConfirmingReinstall);
            Assert.Empty(app.Launcher.Calls);

            await app.Model.ReinstallHelperAsync();
            Assert.Equal(["C:\\Plaitway\\app\\plaitwayd.exe install -update -start"], app.Launcher.Calls);
        });
    }

    [Fact]
    public async Task ADaemonOfTheDeveloperOrOneThatIsDownIsNotAnExternalHelper()
    {
        await using var overridden = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled, IsOverridden = true });
        await overridden.RunAsync(() =>
        {
            Assert.False(overridden.Model.IsHelperExternal);
            return Task.CompletedTask;
        });

        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            Assert.True(app.Model.IsHelperExternal);
            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == Daemon.DaemonConnection.Unavailable);
            Assert.False(app.Model.IsHelperExternal);
            Assert.Equal(SetupKind.NeedsInstall, app.Model.Setup.Kind);
        });
    }

    // start with Windows

    [Fact]
    public async Task ALaunchAtLoginThatCannotBeChangedIsReported()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(() =>
        {
            app.Model.SetLaunchAtLogin(true);
            Assert.True(app.Model.LaunchAtLogin);
            Assert.Null(app.Model.Alert);

            app.Startup.Failure = new InvalidOperationException("the startup list is read-only");
            app.Model.SetLaunchAtLogin(false);
            Assert.True(app.Model.LaunchAtLogin, "it shows what the list says, not what was asked");
            Assert.Equal(AppAlert.TitleText(AlertTitle.LaunchAtLoginFailed, app.Text), app.Model.Alert?.Title);
            return Task.CompletedTask;
        });
    }

    // quitting

    [Fact]
    public async Task ProfilesThatAreOnAreWhatQuittingLeavesRunning()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var office = await app.ImportFixtureAsync("office.ovpn");
            var lab = await app.ImportFixtureAsync("lab.ovpn");
            await app.ImportFixtureAsync("idle.ovpn");
            Assert.Empty(app.Model.SwitchedOnProfiles);

            await app.Store.SetEnabledAsync(office, enabled: true);
            await app.Store.SetEnabledAsync(lab, enabled: true);
            await Wait.UntilAsync("both connected", () => new[] { office, lab }.All(id => app.Store.Find(id)?.State == ProfileState.Connected));
            Assert.Equal(new HashSet<string> { office, lab }, app.Model.SwitchedOnProfiles.Select(profile => profile.Id).ToHashSet());

            Assert.True(await app.Model.DisconnectAllAsync());
            await Wait.UntilAsync("both disconnected", () => new[] { office, lab }.All(id => app.Store.Find(id)?.State == ProfileState.Disconnected));
            Assert.Empty(app.Model.SwitchedOnProfiles);
            Assert.Null(app.Model.Alert);

            // A helper that does not answer says nothing true about its profiles.
            await app.Store.SetEnabledAsync(office, enabled: true);
            await Wait.UntilAsync("connected again", () => app.Store.Find(office)?.State == ProfileState.Connected);
            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == Daemon.DaemonConnection.Unavailable);
            Assert.Empty(app.Model.SwitchedOnProfiles);
        });
    }

    [Fact]
    public async Task QuitAsksWhatToDoWithTheProfilesThatAreOn()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var prompts = new FakeQuitPrompts();
            Assert.Equal(new QuitDecision(CanQuit: true, ShowWindow: false), await app.Model.RequestQuitAsync(prompts, isPoweringOff: false));
            Assert.Equal(0, prompts.QuitAsked);

            var id = await app.ImportFixtureAsync("office.ovpn");
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("connected", () => app.Store.Find(id)?.State == ProfileState.Connected);

            prompts.Answer = QuitAnswer.Cancel;
            Assert.Equal(new QuitDecision(CanQuit: false, ShowWindow: false), await app.Model.RequestQuitAsync(prompts, isPoweringOff: false));

            prompts.Answer = QuitAnswer.Quit;
            Assert.Equal(new QuitDecision(CanQuit: true, ShowWindow: false), await app.Model.RequestQuitAsync(prompts, isPoweringOff: false));
            Assert.Equal(ProfileState.Connected, app.Store.Find(id)?.State);

            // Windows signing out does not wait for an answer.
            var before = prompts.QuitAsked;
            Assert.Equal(new QuitDecision(CanQuit: true, ShowWindow: false), await app.Model.RequestQuitAsync(prompts, isPoweringOff: true));
            Assert.Equal(before, prompts.QuitAsked);

            prompts.Answer = QuitAnswer.DisconnectAndQuit;
            Assert.Equal(new QuitDecision(CanQuit: true, ShowWindow: false), await app.Model.RequestQuitAsync(prompts, isPoweringOff: false));
            await Wait.UntilAsync("disconnected", () => app.Store.Find(id)?.State == ProfileState.Disconnected);
        });
    }

    [Fact]
    public async Task QuitAsksBeforeEditsThatWereNotSavedAreLost()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
            var editor = app.Model.EditorFor(app.Store.Find(id)!);
            await editor.LoadAsync(app.Store);
            editor.Text += "# unsaved\n";
            var prompts = new FakeQuitPrompts { DiscardEdits = false };

            var decision = await app.Model.RequestQuitAsync(prompts, isPoweringOff: false);

            Assert.Equal(new QuitDecision(CanQuit: false, ShowWindow: true), decision);
            Assert.Equal(1, prompts.DiscardAsked);

            prompts.DiscardEdits = true;
            Assert.True((await app.Model.RequestQuitAsync(prompts, isPoweringOff: false)).CanQuit);
            Assert.True((await app.Model.RequestQuitAsync(prompts, isPoweringOff: true)).CanQuit);
        });
    }

    // renaming

    [Fact]
    public async Task ARefusedRenameSaysSoSoTheFieldCanGoBack()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");

            Assert.True(await app.Model.RenameAsync(id, "Head Office"));
            await Wait.UntilAsync("the rename", () => app.Store.Find(id)?.Name == "Head Office");
            Assert.True(await app.Model.RenameAsync(id, " Head Office "), "the name it has already is not a refusal");
            Assert.False(await app.Model.RenameAsync(id, "   "), "an empty name is not applied");

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == Daemon.DaemonConnection.Unavailable);
            Assert.False(await app.Model.RenameAsync(id, "Other"));
            Assert.Equal(AppAlert.TitleText(AlertTitle.SaveFailed, app.Text), app.Model.Alert?.Title);
        });
    }

    // logs and diagnostics

    [Fact]
    public async Task FollowsAProfilesLogAndStopsWhenCancelled()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            await app.Store.SetEnabledAsync(id, enabled: true);
            var tail = new LogTail(TimeProvider.System);
            using var stop = new CancellationTokenSource();
            var following = tail.RunAsync(app.Store, id, stop.Token);

            await Wait.UntilAsync("the connection to be logged", () => tail.Entries.Any(entry => entry.Text.StartsWith("connected on", StringComparison.Ordinal)));
            Assert.Contains("connected on", tail.PlainText, StringComparison.Ordinal);
            await stop.CancelAsync();
            await following;
        });
    }

    [Fact]
    public async Task ALogOfADeletedProfileEnds()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var tail = new LogTail(TimeProvider.System);

            // NotFound ends the loop instead of retrying forever.
            await tail.RunAsync(app.Store, "nope", CancellationToken.None);

            Assert.Empty(tail.Entries);
        });
    }

    [Fact]
    public async Task ReadsDiagnosticsAndRefreshesAfterACommand()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var diagnostics = new DiagnosticsModel(app.Store, app.Model.Errors, TimeProvider.System);
            using var stop = new CancellationTokenSource();
            var polling = diagnostics.RunAsync(stop.Token);
            await Wait.UntilAsync("the first reading", () => diagnostics.Reading is not null);
            Assert.Equal(["fake-stale-1"], diagnostics.Reading!.StaleRoutes.Select(route => route.Key));

            await app.Model.RemoveStaleRouteAsync("fake-stale-1");
            await diagnostics.RefreshAsync();
            Assert.Empty(diagnostics.Reading!.StaleRoutes);
            Assert.Null(app.Model.Alert);

            // Removing it again finds it gone already, which is what was asked for.
            await app.Model.RemoveStaleRouteAsync("fake-stale-1");
            Assert.True(app.Model.Alert is null, $"an alert for a route that is already gone: {app.Model.Alert?.Message}");
            await app.Model.ResyncAsync();
            await diagnostics.RefreshAsync();
            Assert.Equal("manual", diagnostics.Reading!.Network.LastChangeReason);
            await stop.CancelAsync();
            await polling;
        });
    }
}
