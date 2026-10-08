using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Dialogs;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.Tray;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>The tray menu and its icon, and the dialogs that the model asks for.</summary>
public sealed class TrayAndDialogTests(DaemonBinary binary)
{
    private static readonly IReadOnlyList<Profile> Listed =
    [
        Profiles.Make("a", "Router", ProfileState.Connected, desired: true, ProfileKind.Openvpn),
        Profiles.Make("b", "Home", ProfileState.Disconnected, kind: ProfileKind.Wireguard),
    ];

    [Fact]
    public void TheMenuListsTheCountTheProfilesDisconnectAllAndTheWindowCommands()
    {
        var text = TestText.En;

        var entries = TrayMenuBuilder.Build(new MenuModel(DaemonSetup.Ready, Listed, text), text);

        Assert.Equal(
            [TrayCommand.None, TrayCommand.ChooseProfile, TrayCommand.ChooseProfile, TrayCommand.None, TrayCommand.DisconnectAll, TrayCommand.None, TrayCommand.Open, TrayCommand.Import, TrayCommand.Settings, TrayCommand.None, TrayCommand.Quit],
            entries.Select(entry => entry.Command));
        Assert.Equal("Connected: 1", entries[0].Text);
        Assert.False(entries[0].IsEnabled);
        Assert.Equal(["Router", "Home"], entries.Where(entry => entry.Command == TrayCommand.ChooseProfile).Select(entry => entry.Text));
        Assert.Equal(["OpenVPN · Connected", "WireGuard · Disconnected"], entries.Where(entry => entry.Command == TrayCommand.ChooseProfile).Select(entry => entry.Detail));
        Assert.Equal(MenuMark.On, entries[1].Mark);
        Assert.Equal(["Disconnect All", "Open Plaitway", "Import Profile…", "Settings…", "Quit Plaitway"], entries.Where(entry => entry.Text.Length > 0 && entry.Command is not (TrayCommand.None or TrayCommand.ChooseProfile)).Select(entry => entry.Text));
        Assert.Single(entries, entry => entry.IsDefault);
        Assert.True(entries.Single(entry => entry.IsDefault).Command == TrayCommand.Open);
    }

    [Fact]
    public void WithoutAnAnsweringHelperTheMenuSaysSoAndOffersNoProfiles()
    {
        var text = TestText.En;

        var entries = TrayMenuBuilder.Build(new MenuModel(DaemonSetup.NotResponding(), Listed, text), text);

        Assert.Equal("Helper unavailable", entries[0].Text);
        Assert.DoesNotContain(entries, entry => entry.Command is TrayCommand.ChooseProfile or TrayCommand.DisconnectAll);
        Assert.False(entries.Single(entry => entry.Command == TrayCommand.Import).IsEnabled);
        Assert.True(entries.Single(entry => entry.Command == TrayCommand.Quit).IsEnabled);
    }

    [Fact]
    public void TheMenuInChineseUsesTheWordsOfTheMacApp()
    {
        var text = TestText.Zh;

        var entries = TrayMenuBuilder.Build(new MenuModel(DaemonSetup.Ready, Listed, text), text);

        Assert.Contains(entries, entry => entry.Text == "全部中斷連線");
        Assert.Contains(entries, entry => entry.Text == "打開 Plaitway");
        Assert.Contains(entries, entry => entry.Text == "匯入設定檔…");
        Assert.Contains(entries, entry => entry.Text == "結束 Plaitway");
        Assert.Equal("已連線：1", entries[0].Text);
    }

    [Fact]
    public async Task ARowOfTheMenuDoesWhatItsProfileNeeds()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var tray = new TrayViewModel(app.Model);
            var requests = new List<ShellRequest>();
            tray.Requested += requests.Add;
            var office = await app.ImportFixtureAsync("office.ovpn");
            var needsCredentials = await app.ImportFixtureAsync("router.ovpn", Fixture.OpenVpn(markers: ["# fake: needs-credentials"]));

            await tray.InvokeAsync(tray.Entries.Single(entry => entry.ProfileId == office));
            await app.WaitForStateAsync(office, ProfileState.Connected);
            Assert.Equal(AggregateState.Connected, tray.State);

            await tray.InvokeAsync(tray.Entries.Single(entry => entry.ProfileId == office));
            await app.WaitForStateAsync(office, ProfileState.Disconnected);

            // A profile that waits for a password has its dialog in the window.
            await app.Store.SetEnabledAsync(needsCredentials, enabled: true);
            await app.WaitForStateAsync(needsCredentials, ProfileState.AwaitingCredentials);
            await tray.InvokeAsync(tray.Entries.Single(entry => entry.ProfileId == needsCredentials));
            Assert.Equal([ShellRequest.ShowWindow], requests);
            Assert.Equal(AggregateState.NeedsCredentials, tray.State);
        });
    }

    [Fact]
    public async Task TheWindowCommandsOfTheMenuAreRequestsToTheWindow()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var tray = new TrayViewModel(app.Model);
            var requests = new List<ShellRequest>();
            tray.Requested += requests.Add;

            foreach (var command in new[] { TrayCommand.Open, TrayCommand.Import, TrayCommand.Settings, TrayCommand.Quit })
            {
                await tray.InvokeAsync(tray.Entries.Single(entry => entry.Command == command));
            }

            tray.Select();
            Assert.Equal([ShellRequest.ShowWindow, ShellRequest.ImportProfile, ShellRequest.ShowSettings, ShellRequest.Quit, ShellRequest.ShowWindow], requests);
        });
    }

    [Fact]
    public async Task TheMenuIsNotMadeAgainForAByteCount()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var tray = new TrayViewModel(app.Model);
            var id = await app.ImportFixtureAsync("office.ovpn");
            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);
            var entries = tray.Entries;
            var rebuilt = 0;
            tray.PropertyChanged += (_, args) => rebuilt += args.PropertyName == nameof(TrayViewModel.Entries) ? 1 : 0;

            // The daemon reports the counters every couple of seconds.
            await Task.Delay(2_500);

            Assert.Same(entries, tray.Entries);
            Assert.Equal(0, rebuilt);
        });
    }

    [Fact]
    public async Task TheIconAndTheTooltipFollowTheProfilesAndTheHelper()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var tray = new TrayViewModel(app.Model);
            Assert.Equal(AggregateState.Idle, tray.State);
            Assert.Equal("Plaitway · Not connected", tray.Tooltip);

            var id = await app.ImportFixtureAsync("broken.ovpn", Fixture.OpenVpn(markers: ["# fake: fail"]));
            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Failed);
            Assert.Equal(AggregateState.Problem, tray.State);

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == DaemonConnection.Unavailable);
            Assert.Equal(AggregateState.Unavailable, tray.State);
            Assert.Equal("Plaitway · Helper unavailable", tray.Tooltip);
        });
    }

    // dialogs

    [Fact]
    public async Task ACommandThatFailedIsShownOnceAndThenClearedWhateverTheUserPresses()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs();
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();

            app.Model.Report(new InvalidOperationException("it did not work"), AlertTitle.SaveFailed);
            await Wait.UntilAsync("the alert to go", () => app.Model.Alert is null);

            var question = Assert.Single(dialogs.Questions);
            Assert.Equal("Save failed", question.Title);
            Assert.Equal("it did not work", question.Message);
            Assert.Equal(["Close"], question.Choices.Select(choice => choice.Label));
        });
    }

    [Fact]
    public async Task DeletingAProfileAsksFirstAndNamesTheAction()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            var dialogs = new FakeDialogs();
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();

            // A no: nothing is deleted.
            app.Model.RequestDeletion(id);
            await Wait.UntilAsync("the question to be answered", () => app.Model.PendingDeletion is null);
            var question = Assert.Single(dialogs.Questions);
            Assert.Equal("Delete “office”?", question.Title);
            Assert.Equal("The profile and its saved credentials are removed.", question.Message);
            Assert.Equal(["Delete", "Cancel"], question.Choices.Select(choice => choice.Label));
            Assert.Equal(ChoiceRole.Destructive, question.Choices[0].Role);
            Assert.NotNull(app.Store.Find(id));

            // A yes.
            dialogs.Answer = _ => 0;
            app.Model.RequestDeletion(id);
            await Wait.UntilAsync("the profile to go", () => app.Store.Find(id) is null);
        });
    }

    [Fact]
    public async Task ReinstallingAndUninstallingTheHelperAskFirst()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs { Answer = _ => 0 };
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();

            app.Model.IsConfirmingReinstall = true;
            await Wait.UntilAsync("the reinstall", () => app.Launcher.Calls.Count == 1);
            Assert.EndsWith("install -update -start", app.Launcher.Calls[0], StringComparison.Ordinal);
            Assert.Equal("Reinstall helper?", dialogs.Questions[0].Title);
            Assert.Equal("Connected profiles disconnect.", dialogs.Questions[0].Message);

            app.Model.IsConfirmingUninstall = true;
            await Wait.UntilAsync("the uninstall", () => app.Launcher.Calls.Count == 2);
            Assert.EndsWith("uninstall", app.Launcher.Calls[1], StringComparison.Ordinal);
            Assert.Equal("Uninstall helper?", dialogs.Questions[1].Title);
            Assert.Contains(HelperPaths.LogPath, dialogs.Questions[1].Message, StringComparison.Ordinal);

            // A no runs nothing.
            dialogs.Answer = request => request.DismissIndex;
            app.Model.IsConfirmingUninstall = true;
            await Wait.UntilAsync("the answer", () => !app.Model.IsConfirmingUninstall);
            Assert.Equal(2, app.Launcher.Calls.Count);
        });
    }

    [Fact]
    public async Task APromptIsAnsweredFromTheDialogAndNotShownAgainWhileItWaits()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs
            {
                OnCredentials = async prompt =>
                {
                    Assert.Equal("Credentials", prompt.Title);
                    Assert.True(prompt.AsksForUsername);
                    Assert.Equal("router", prompt.ProfileName);
                    Assert.False(prompt.SubmitCommand.CanExecute(null));
                    prompt.Username = "alice";
                    prompt.Password = "s3cret";
                    Assert.True(prompt.SubmitCommand.CanExecute(null));
                    await prompt.SubmitCommand.ExecuteAsync(null);
                    Assert.Equal(string.Empty, prompt.Password);
                },
            };
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();
            var id = await app.ImportFixtureAsync("router.ovpn", Fixture.OpenVpn(markers: ["# fake: needs-credentials"]));

            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);

            Assert.Single(dialogs.Credentials);
            Assert.Equal(new Credentials("alice", "s3cret"), await saved.GetAsync(id, CredentialKind.UserPassword));
        });
    }

    [Fact]
    public async Task ARefusedPasswordBringsTheDialogBackWithTheReason()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var attempts = 0;
            var rejections = new List<string>();
            var dialogs = new FakeDialogs
            {
                OnCredentials = async prompt =>
                {
                    rejections.Add(prompt.RejectionMessage);
                    prompt.Username = "alice";
                    prompt.Password = attempts++ == 0 ? "wrong" : "right";
                    await prompt.SubmitCommand.ExecuteAsync(null);
                },
            };
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();
            var id = await app.ImportFixtureAsync("router.ovpn", Fixture.OpenVpn(markers: ["# fake: needs-credentials"]));

            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);

            Assert.Equal(["", "authentication failed"], rejections);
        });
    }

    [Fact]
    public async Task CancellingTheDialogStopsTheProfile()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs { OnCredentials = prompt => prompt.CancelCommand.ExecuteAsync(null) };
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();
            var id = await app.ImportFixtureAsync("router.ovpn", Fixture.OpenVpn(markers: ["# fake: needs-credentials"]));

            await app.Store.SetEnabledAsync(id, enabled: true);

            await app.WaitForStateAsync(id, ProfileState.Disconnected);
            Assert.False(app.Store.Find(id)!.DesiredEnabled);
        });
    }

    [Fact]
    public async Task AKeyPassphraseHasNoUserNameAndTheDialogClosesWhenTheDaemonStopsAsking()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", Fixture.OpenVpn(markers: ["# fake: needs-credentials"]));
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the prompt", () => app.Store.CredentialPrompts.Count > 0);
            using var dialog = new CredentialPromptViewModel(app.Model, app.Store.CredentialPrompts[0] with { Kind = CredentialKind.KeyPassphrase });

            Assert.Equal("Key passphrase", dialog.Title);
            Assert.False(dialog.AsksForUsername);
            Assert.Equal("Passphrase", dialog.SecretLabel);
            dialog.Password = "phrase";
            Assert.True(dialog.CanSubmit);
            Assert.False(dialog.Completion.IsCompleted);

            await app.Store.SetEnabledAsync(id, enabled: false);
            await dialog.Completion.WaitAsync(TimeSpan.FromSeconds(10));
        });
    }

    [Fact]
    public async Task WhatAnImportFoundIsShownAfterTheDialogsThatNeedTheUserMore()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["fine.ovpn"] = "client\nremote vpn.example.net 1194\n",
            ["scripted.ovpn"] = "client\nremote vpn.example.net 1194\nup /bin/sh\n",
            ["bad.ovpn"] = "client\n# fake: reject\n",
        });
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs();
            using var coordinator = new DialogCoordinator(app.Model, dialogs);
            coordinator.Start();

            string[] names = ["fine.ovpn", "scripted.ovpn", "bad.ovpn"];
            await app.Model.ImportFilesAsync(names.Select(directory.Resolve));
            await Wait.UntilAsync("the report", () => dialogs.Reports.Count == 1 && app.Model.ImportReport is null);

            var rows = dialogs.Reports[0].Rows;
            Assert.Equal(["fine.ovpn", "scripted.ovpn", "bad.ovpn"], rows.Select(row => row.Filename));
            Assert.Equal([StatusIcon.RouteInstalled, StatusIcon.RouteBlocked, StatusIcon.RouteFailed], rows.Select(row => row.Icon));
            Assert.Equal([StatusTone.Success, StatusTone.Caution, StatusTone.Critical], rows.Select(row => row.Tone));
            Assert.Contains("up", rows[1].Details[0], StringComparison.Ordinal);
            Assert.Contains("rejected", rows[2].Details[0], StringComparison.Ordinal);
        });
    }

    [Fact]
    public async Task TheQuitQuestionsNameTheirActions()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var dialogs = new FakeDialogs();
            var prompts = new QuitPrompts(app.Text, dialogs);

            dialogs.Answer = _ => 1;
            Assert.Equal(QuitAnswer.DisconnectAndQuit, await prompts.AskAsync());
            dialogs.Answer = _ => 0;
            Assert.Equal(QuitAnswer.Quit, await prompts.AskAsync());
            dialogs.Answer = request => request.DismissIndex;
            Assert.Equal(QuitAnswer.Cancel, await prompts.AskAsync());
            Assert.False(await prompts.ConfirmDiscardingEditsAsync());

            Assert.Equal(["Quit", "Disconnect All and Quit", "Cancel"], dialogs.Questions[0].Choices.Select(choice => choice.Label));
            Assert.Equal("Quit Plaitway?", dialogs.Questions[0].Title);
            Assert.Equal("Connected profiles stay connected.", dialogs.Questions[0].Message);
            Assert.Equal("Quit with unsaved changes?", dialogs.Questions[3].Title);
        });
    }
}
