using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Dialogs;
using Plaitway.AppCore.Editing;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>The pages of a profile, of Diagnostics and of Settings, against the real daemon.</summary>
public sealed class PageViewModelTests(DaemonBinary binary)
{
    private const string PrivateKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

    [Fact]
    public async Task TheOverviewSaysWhatTheDaemonSaysAndHidesWhatIsEmpty()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
            var clipboard = new FakeClipboard();
            using var overview = new OverviewViewModel(app.Model, id, clipboard);

            // Not connected: the endpoint and the addresses the profile names itself.
            Assert.Equal("Disconnected", overview.StateLabel);
            Assert.False(overview.ShowsUptime);
            Assert.False(overview.ShowsTraffic);
            Assert.False(overview.NeedsAttention);
            Assert.Contains(overview.Details, row => row.Label == "Remote" && row.Value == "203.0.113.5:51820");
            Assert.DoesNotContain(overview.Details, row => row.Label == "Interface");
            Assert.True(overview.HasPublicKey);

            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);

            Assert.Equal("Connected", overview.StateLabel);
            Assert.Equal(StatusIcon.Connected, overview.Icon);
            Assert.Equal(StatusTone.Success, overview.Tone);
            Assert.True(overview.ShowsTraffic);
            Assert.True(overview.ShowsUptime);
            Assert.Matches(@"^\d+:\d\d:\d\d$", overview.UptimeText);
            Assert.Contains(overview.Details, row => row.Label == "Interface" && row.Value.Length > 0);

            overview.CopyPublicKeyCommand.Execute(null);
            Assert.Equal(app.Store.Find(id)!.Summary.PublicKey, clipboard.Text);

            overview.ShowRoutesCommand.Execute(null);
            Assert.Equal(ProfileSection.Routes, app.Model.ProfileSection);
        });
    }

    [Fact]
    public async Task AFailedProfileShowsItsCauseAsAnError()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("broken.ovpn", Fixture.OpenVpn(markers: ["# fake: fail"]));
            using var overview = new OverviewViewModel(app.Model, id, new FakeClipboard());

            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Failed);

            Assert.True(overview.IsFailed);
            Assert.True(overview.HasCause);
            Assert.Equal(app.Store.Find(id)!.LastError, overview.Cause);
            Assert.Equal(StatusTone.Critical, overview.Tone);
        });
    }

    [Fact]
    public void TheTrafficChartIsOneScaleForBothDirections()
    {
        var start = DateTimeOffset.FromUnixTimeSeconds(1_000);
        var chart = TrafficChart.Of(
        [
            new TrafficSample(1, start, Received: 2_000, Sent: 500),
            new TrafficSample(2, start.AddSeconds(2), Received: 4_000, Sent: 1_000),
            new TrafficSample(3, start.AddSeconds(4), Received: 1_000, Sent: 250),
        ]);

        Assert.Equal([0.0, 0.5, 1.0], chart.Received.Select(point => point.X));
        Assert.Equal([0.5, 1.0, 0.25], chart.Received.Select(point => point.Y));
        Assert.Equal([0.125, 0.25, 0.0625], chart.Sent.Select(point => point.Y));

        // A trickle does not fill the chart.
        var quiet = TrafficChart.Of([new TrafficSample(1, start, Received: 100, Sent: 0), new TrafficSample(2, start.AddSeconds(2), Received: 100, Sent: 0)]);
        Assert.True(quiet.Received[0].Y < 0.1);
        Assert.Same(TrafficChart.Empty, TrafficChart.Of([]));
    }

    [Fact]
    public async Task TheRoutesTabListsWhatTheProfileNamesWhileItIsNotConnected()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            var clipboard = new FakeClipboard();
            using var routes = new RoutesViewModel(app.Model, id, clipboard);

            Assert.Contains("192.168.1.0/24", routes.Routes.Select(row => row.Prefix));
            Assert.All(routes.Routes, row => Assert.Null(row.State));
            Assert.False(routes.IsEmpty);

            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);

            Assert.All(routes.Routes, row => Assert.NotNull(row.State));

            routes.CopyPrefixesCommand.Execute(routes.Routes.Select(row => row.Id).ToList());
            Assert.Equal(string.Join('\n', routes.Routes.Select(row => row.Prefix)), clipboard.Text);
        });
    }

    [Fact]
    public async Task TheLogsTabFiltersByLevelAndSearch()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            var clipboard = new FakeClipboard();
            using var logs = new LogsViewModel(app.Model, id, clipboard);
            Assert.Equal(LogFilter.Info, logs.SelectedChoice.Filter);
            Assert.Equal(["All levels", "Info and above", "Warnings and errors", "Errors only"], logs.Choices.Select(choice => choice.Label));

            logs.Activate();
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the connection to be logged", () => logs.Lines.Any(line => line.Text.StartsWith("connected on", StringComparison.Ordinal)));

            logs.Query = "no such text anywhere";
            Assert.Empty(logs.Lines);
            Assert.True(logs.ShowsNoMatch);

            logs.ClearQueryCommand.Execute(null);
            Assert.NotEmpty(logs.Lines);

            logs.SelectedChoice = logs.Choices[(int)LogFilter.Errors];
            Assert.All(logs.Lines, line => Assert.Equal(LogLevel.Error, line.Level));

            logs.SelectedChoice = logs.Choices[(int)LogFilter.All];
            logs.CopyCommand.Execute(null);
            Assert.Equal(string.Join('\n', logs.Lines.Select(line => line.PlainText)), clipboard.Text);

            logs.Deactivate();
            await logs.StoppedAsync();
        });
    }

    [Fact]
    public void TheLevelFiltersIncludeWhatTheirWordsSay()
    {
        Assert.True(LogFilter.All.Includes(LogLevel.Debug));
        Assert.False(LogFilter.Info.Includes(LogLevel.Debug));
        Assert.True(LogFilter.Info.Includes(LogLevel.Unspecified));
        Assert.True(LogFilter.Warnings.Includes(LogLevel.Warn) && LogFilter.Warnings.Includes(LogLevel.Error));
        Assert.False(LogFilter.Warnings.Includes(LogLevel.Info));
        Assert.True(LogFilter.Errors.Includes(LogLevel.Error));
        Assert.False(LogFilter.Errors.Includes(LogLevel.Warn));
    }

    [Fact]
    public async Task TheConfigurationTabEditsSavesAndHidesTheSecretsWhenLeft()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
            using var configuration = new ConfigurationViewModel(app.Model, id);
            Assert.True(configuration.IsLoading);

            configuration.Activate();
            await Wait.UntilAsync("the text", () => configuration.IsReady);
            Assert.DoesNotContain(PrivateKey, configuration.Content, StringComparison.Ordinal);
            Assert.Contains("\u2039secret 1\u203A", configuration.Content, StringComparison.Ordinal);
            Assert.Equal("Show Secrets", configuration.SecretsLabel);
            Assert.False(configuration.CanSave);

            configuration.ToggleSecretsCommand.Execute(null);
            Assert.Contains(PrivateKey, configuration.Content, StringComparison.Ordinal);
            Assert.Equal("Hide Secrets", configuration.SecretsLabel);

            configuration.Content = configuration.Content.Replace("10.6.0.2/32", "10.6.0.9/32", StringComparison.Ordinal);
            Assert.True(configuration.IsDirty);
            Assert.True(configuration.CanSave);

            // Leaving the page hides the keys again, with the edit made meanwhile.
            configuration.Deactivate();
            Assert.False(configuration.ShowsSecrets);
            Assert.DoesNotContain(PrivateKey, configuration.Content, StringComparison.Ordinal);
            Assert.True(configuration.IsDirty);

            await configuration.SaveCommand.ExecuteAsync(null);
            Assert.False(configuration.IsDirty);
            Assert.Contains("Address = 10.6.0.9/32", await app.Store.GetProfileContentAsync(id), StringComparison.Ordinal);
        });
    }

    [Fact]
    public async Task TheConfigurationTabMarksARefusedLine()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn", Fixture.OpenVpn());
            using var configuration = new ConfigurationViewModel(app.Model, id);
            configuration.Activate();
            await Wait.UntilAsync("the text", () => configuration.IsReady);
            configuration.Content += "# fake: reject\n";

            await configuration.SaveCommand.ExecuteAsync(null);

            Assert.True(configuration.HasDiagnostic);
            Assert.Equal(configuration.Content.Split('\n').Length - 1, configuration.DiagnosticLine);
            Assert.Equal($"Line {configuration.DiagnosticLine}", configuration.DiagnosticPlace);
            Assert.False(string.IsNullOrEmpty(configuration.DiagnosticMessage));
            Assert.True(configuration.IsDirty);
            Assert.Null(app.Model.Alert);
        });
    }

    [Fact]
    public async Task ASaveOfARunningProfileOffersToRestartItAndSaysTheOldTextRunsUntilThen()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("home.conf", Fixture.WireGuard());
            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);
            using var configuration = new ConfigurationViewModel(app.Model, id);
            configuration.Activate();
            await Wait.UntilAsync("the text", () => configuration.IsReady);
            Assert.True(configuration.OffersReconnect);

            configuration.Content += "# a note\n";
            await configuration.SaveCommand.ExecuteAsync(null);
            Assert.True(configuration.RunsOldText);

            configuration.Content += "# another\n";
            await configuration.SaveAndReconnectCommand.ExecuteAsync(null);
            Assert.False(configuration.RunsOldText);
        });
    }

    [Fact]
    public async Task TheSettingsTabChangesOneSettingAtATime()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var a = await app.ImportFixtureAsync("a.conf", Fixture.WireGuard());
            var b = await app.ImportFixtureAsync("b.conf", Fixture.WireGuard());
            using var settings = new ProfileSettingsViewModel(app.Model, b);
            Assert.Equal("b", settings.Name);
            Assert.Equal(2, settings.Position);
            Assert.True(settings.CanMoveUp);
            Assert.False(settings.CanMoveDown);
            Assert.True(settings.OffersExcludePrivateIps);
            Assert.Equal("Auto", settings.SelectedTunnelMode.Label);

            // Showing the daemon's values changed nothing at the daemon.
            await Task.Delay(100);
            Assert.False(app.Store.Find(b)!.Settings.AutoConnect);

            settings.AutoConnect = true;
            await Wait.UntilAsync("auto-connect", () => app.Store.Find(b)?.Settings.AutoConnect == true);
            settings.SelectedTunnelMode = settings.TunnelModes.First(choice => choice.Mode == TunnelMode.Split);
            await Wait.UntilAsync("split tunnel", () => app.Store.Find(b)?.Settings.TunnelMode == TunnelMode.Split);
            settings.OnDemandWifi = true;
            await Wait.UntilAsync("Wi-Fi", () => app.Store.Find(b)?.Settings.OnDemand?.Wifi == true);

            Assert.True(app.Store.Find(b)!.Settings.AutoConnect);
            Assert.False(app.Store.Find(b)!.Settings.OnDemand.Ethernet);

            await settings.MoveUpCommand.ExecuteAsync(null);
            await Wait.UntilAsync("b first", () => app.Store.Profiles.Select(profile => profile.Id).SequenceEqual([b, a]));
            Assert.Equal(1, settings.Position);
            Assert.False(settings.CanMoveUp);
        });
    }

    [Fact]
    public async Task ARenameTheDaemonRefusesPutsTheOldNameBack()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            using var settings = new ProfileSettingsViewModel(app.Model, id);

            settings.Name = "  Head Office  ";
            await settings.CommitNameCommand.ExecuteAsync(null);
            await Wait.UntilAsync("the rename", () => app.Store.Find(id)?.Name == "Head Office");

            settings.Name = "   ";
            await settings.CommitNameCommand.ExecuteAsync(null);
            Assert.Equal("Head Office", settings.Name);
        });
    }

    [Fact]
    public async Task TheSettingsTabForgetsSavedCredentialsAndAsksBeforeDeleting()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn");
            await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);
            using var settings = new ProfileSettingsViewModel(app.Model, id);
            await Wait.UntilAsync("the lookup", () => settings.HasSavedCredentials);

            await settings.ForgetCredentialsCommand.ExecuteAsync(null);
            Assert.False(settings.HasSavedCredentials);
            Assert.False(await saved.ContainsAsync(id, CredentialKind.UserPassword));

            settings.RequestDeletionCommand.Execute(null);
            Assert.Equal(id, app.Model.PendingDeletion);
            Assert.NotNull(app.Store.Find(id));
        });
    }

    [Fact]
    public async Task DiagnosticsListsWhatNeedsDoingFirstAndRemovesAStaleRouteAfterAsking()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs { Answer = _ => 0 };
            var clipboard = new FakeClipboard();
            using var diagnostics = new DiagnosticsViewModel(app.Model, clipboard, dialogs);
            diagnostics.Activate();
            await Wait.UntilAsync("the reading", () => diagnostics.HasReading);

            Assert.True(diagnostics.HasStaleRoutes);
            var stale = diagnostics.StaleRoutes[0];
            Assert.Equal("fake-stale-1", stale.Key);
            Assert.NotEmpty(diagnostics.Network);

            diagnostics.CopyReportCommand.Execute(null);
            Assert.StartsWith("Plaitway ", clipboard.Text, StringComparison.Ordinal);
            Assert.DoesNotContain("INFO", clipboard.Text, StringComparison.Ordinal);

            await diagnostics.RemoveStaleRouteCommand.ExecuteAsync(stale);

            Assert.Equal($"Remove route {stale.Prefix}?", Assert.Single(dialogs.Questions).Title);
            Assert.False(diagnostics.HasStaleRoutes);

            await diagnostics.ResyncCommand.ExecuteAsync(null);
            Assert.Contains(diagnostics.Network, row => row.Label == "Last change" && row.Value.EndsWith("manual", StringComparison.Ordinal));
        });
    }

    [Fact]
    public async Task ASaidNoToRemovingAStaleRouteLeavesItAlone()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs();
            using var diagnostics = new DiagnosticsViewModel(app.Model, new FakeClipboard(), dialogs);
            diagnostics.Activate();
            await Wait.UntilAsync("the reading", () => diagnostics.HasReading);

            await diagnostics.RemoveStaleRouteCommand.ExecuteAsync(diagnostics.StaleRoutes[0]);
            await diagnostics.RefreshAsync();

            Assert.True(diagnostics.HasStaleRoutes);
        });
    }

    [Fact]
    public async Task DiagnosticsPollsOnlyWhileTheOverviewIsOnScreen()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var diagnostics = new DiagnosticsViewModel(app.Model, new FakeClipboard(), new FakeDialogs());
            Assert.Equal(["Overview", "Helper log"], diagnostics.Tabs.Select(tab => tab.Label));
            Assert.False(diagnostics.HasReading);

            // Not shown yet: nothing is read.
            await Task.Delay(300);
            Assert.False(diagnostics.HasReading);

            diagnostics.Activate();
            await Wait.UntilAsync("the reading", () => diagnostics.HasReading);

            diagnostics.SelectedTab = diagnostics.Tabs[(int)DiagnosticsPage.DaemonLog];
            Assert.True(diagnostics.ShowsHelperLog);
            await Wait.UntilAsync("the helper's log", () => diagnostics.HelperLog.Lines.Count > 0, TimeSpan.FromSeconds(5));
            diagnostics.Deactivate();
        });
    }

    [Fact]
    public async Task TheSettingsPageShowsTheHelperAndTheVersionsAndOnlyOffersWhatCanBeDone()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { AppVersion = "1.2.3", HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            using var settings = new AppSettingsViewModel(app.Model);
            await Wait.UntilAsync("the daemon's version", () => settings.Helper.Any(row => row.Label == "Helper version"));

            Assert.Equal("Running", settings.Helper.First(row => row.Label == "Status").Value);
            Assert.Contains(settings.Helper, row => row.Label == "App version" && row.Value == "1.2.3");
            Assert.Contains(settings.Helper, row => row.Label == "WireGuard");
            Assert.Contains(settings.Helper, row => row.Label == "OpenVPN");
            Assert.True(settings.CanManageHelper);
            Assert.False(settings.CanStartHelper);
            Assert.True(settings.CanUninstallHelper);

            settings.RequestReinstallCommand.Execute(null);
            Assert.True(app.Model.IsConfirmingReinstall);
            settings.RequestUninstallCommand.Execute(null);
            Assert.True(app.Model.IsConfirmingUninstall);

            settings.LaunchAtLogin = true;
            Assert.True(app.Startup.IsEnabled);
            settings.LaunchAtLogin = false;
            Assert.False(app.Startup.IsEnabled);
        });
    }

    [Fact]
    public async Task ADaemonOfTheDeveloperIsNotManagedFromTheSettingsPage()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { IsOverridden = true, HelperState = HelperState.NotInstalled });
        await app.RunAsync(() =>
        {
            using var settings = new AppSettingsViewModel(app.Model);

            Assert.False(settings.CanManageHelper);
            Assert.False(settings.CanUninstallHelper);
            return Task.CompletedTask;
        });
    }

    [Fact]
    public async Task ADaemonStartedByHandIsCalledInstalledExternally()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(() =>
        {
            using var settings = new AppSettingsViewModel(app.Model);

            Assert.Equal("Installed externally", settings.Helper.First(row => row.Label == "Status").Value);
            Assert.False(settings.CanManageHelper);
            return Task.CompletedTask;
        });
    }
}
