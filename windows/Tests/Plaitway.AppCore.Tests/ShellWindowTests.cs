using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// The window as a whole: whether it is on screen, and the files dropped on it (macos/Sources/PlaitwayMenuBar/RootView.swift,
/// <c>dropDestination</c> and the outline that says a drop imports).
/// </summary>
public sealed class ShellWindowTests(DaemonBinary binary)
{
    private const string Quiet = "client\nremote vpn.example.net 1194\n";

    private static ShellViewModel NewShell(AppHarness app, bool isWindowVisible = true) =>
        new(app.Model, new FakeClipboard(), new FakeFilePicker(), new FakeDialogs(), isWindowVisible);

    private static int LogStreams(AppHarness app) => app.Calls.Calls.Count(call => call == nameof(IDaemonApi.WatchLogsAsync));

    // visibility

    [Fact]
    public async Task AHiddenWindowMakesNoPageWhenTheSelectedProfileIsDeletedByAnotherClient()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var office = await app.ImportFixtureAsync("office.ovpn");
            var lab = await app.ImportFixtureAsync("lab.ovpn");
            shell.Sidebar.SelectProfile(lab);
            app.Model.ProfileSection = ProfileSection.Logs;
            Assert.Equal(lab, Assert.IsType<ProfilePageViewModel>(shell.Page).ProfileId);
            var streamsWhileShown = LogStreams(app);
            Assert.Equal(1, streamsWhileShown);

            shell.SetWindowVisible(false);
            Assert.Null(shell.Page);
            var callsBefore = app.Calls.Calls.Count;

            // What another client does to the profile that was open.
            await app.Model.DeleteAsync(lab);
            await Wait.UntilAsync("the profile to go", () => app.Store.Find(lab) is null);
            await Wait.UntilAsync("the selection to move", () => app.Model.Selection == SidebarItem.Profile(office));
            await Task.Delay(TimeSpan.FromMilliseconds(300));

            Assert.Null(shell.Page);
            Assert.Equal([nameof(IDaemonApi.DeleteProfileAsync)], app.Calls.Calls.Skip(callsBefore));
            Assert.Equal(streamsWhileShown, LogStreams(app));

            shell.SetWindowVisible(true);
            Assert.Equal(office, Assert.IsType<ProfilePageViewModel>(shell.Page).ProfileId);
            Assert.Equal(streamsWhileShown + 1, LogStreams(app));
        });
    }

    [Fact]
    public async Task AWindowThatStartsHiddenMakesNoPageForTheFirstSnapshotUntilItIsShown()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            app.Model.ProfileSection = ProfileSection.Logs;
            using var shell = NewShell(app, isWindowVisible: false);
            Assert.Null(shell.Page);

            var id = await app.ImportFixtureAsync("office.ovpn");
            await Wait.UntilAsync("the profile to be selected", () => app.Model.Selection == SidebarItem.Profile(id));

            Assert.Null(shell.Page);
            Assert.Equal(0, LogStreams(app));

            shell.SetWindowVisible(true);
            Assert.Equal(id, Assert.IsType<ProfilePageViewModel>(shell.Page).ProfileId);
            Assert.Equal(1, LogStreams(app));
        });
    }

    // dropping files

    private static DroppedItem Dropped(TempDirectory directory, string name) => new(name, directory.Resolve(name));

    [Fact]
    public async Task DroppedFilesAreImportedWhateverTheyAreCalledAndTheLastOneIsSelected()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["work.vpn"] = Quiet,
            ["wgs_client-4"] = Fixture.WireGuardText(),
            ["home.WG"] = Fixture.WireGuardText(),
        });

        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);

            string[] names = ["work.vpn", "wgs_client-4", "home.WG"];
            await shell.DropAsync(names.Select(name => Dropped(directory, name)));

            Assert.Equal([ProfileKind.Openvpn, ProfileKind.Wireguard, ProfileKind.Wireguard], app.Store.Profiles.Select(profile => profile.Kind));
            Assert.Null(app.Model.ImportReport);
            Assert.Equal(SidebarItem.Profile(app.Store.Profiles[^1].Id), app.Model.Selection);
        });
    }

    [Fact]
    public async Task ADropReportsWhatTheDaemonRefusedAndWhatIsNotAFileOnThisPc()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["fine.ovpn"] = Quiet,
            ["bad.ovpn"] = "client\n# fake: reject\n",
        });
        Directory.CreateDirectory(directory.Resolve("a folder"));

        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            DroppedItem[] items =
            [
                Dropped(directory, "fine.ovpn"),
                new("inside.zip", null),
                Dropped(directory, "bad.ovpn"),
                new("from the browser", string.Empty),
                Dropped(directory, "a folder"),
                new("blank", "  "),
            ];

            await shell.DropAsync(items);

            var report = Assert.IsType<ImportReport>(app.Model.ImportReport);
            Assert.Equal(["fine"], app.Store.Profiles.Select(profile => profile.Name));
            Assert.Equal(["fine.ovpn", "bad.ovpn", "a folder", "inside.zip", "from the browser", "blank"], report.Outcomes.Select(outcome => outcome.Filename));

            // Every item that was left out is a failure the dialog lists, with the same words.
            var notFiles = report.Outcomes.OfType<ImportOutcome.Failed>().Where(failed => failed.Message == app.Text.NotAFileOnThisPC).Select(failed => failed.Filename);
            Assert.Equal(["inside.zip", "from the browser", "blank"], notFiles);

            // The importer tells a folder from a profile.
            Assert.Contains("a folder", Assert.IsType<ImportOutcome.Failed>(report.Outcomes[2]).Message, StringComparison.Ordinal);
        });
    }

    [Fact]
    public async Task AFileDroppedTwiceIsImportedOnce()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["office.ovpn"] = Quiet });

        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var path = directory.Resolve("office.ovpn");

            await shell.DropAsync([new DroppedItem("office.ovpn", path), new DroppedItem("OFFICE.OVPN", path.ToUpperInvariant())]);

            Assert.Equal(["office"], app.Store.Profiles.Select(profile => profile.Name));
            Assert.Null(app.Model.ImportReport);
        });
    }

    [Fact]
    public async Task NothingDroppedIsNothingToReport()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);

            await shell.DropAsync([]);

            Assert.Null(app.Model.ImportReport);
            Assert.DoesNotContain(nameof(IDaemonApi.ImportProfileAsync), app.Calls.Calls);
        });
    }

    [Fact]
    public async Task TheWindowOutlinesItselfWhileSomethingIsDraggedOverItAndStopsWhenItIsDropped()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["office.ovpn"] = Quiet });

        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var changed = new List<string?>();
            shell.PropertyChanged += (_, args) => changed.Add(args.PropertyName);
            Assert.True(shell.CanAcceptDrop);
            Assert.False(shell.ShowsDropHint);

            shell.SetDropTargeted(true);
            Assert.True(shell.ShowsDropHint);
            Assert.Contains(nameof(ShellViewModel.ShowsDropHint), changed);

            shell.SetDropTargeted(false);
            Assert.False(shell.ShowsDropHint);

            // A drop ends the drag, and no leave event follows it.
            shell.SetDropTargeted(true);
            await shell.DropAsync([Dropped(directory, "office.ovpn")]);
            Assert.False(shell.IsDropTargeted);
            Assert.False(shell.ShowsDropHint);
        });
    }

    [Fact]
    public async Task WhileTheHelperIsNotReadyNothingCanBeDroppedAndNoOutlineIsDrawn()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Stopped });
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["office.ovpn"] = Quiet });

        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => app.Store.Connection == DaemonConnection.Unavailable);
            Assert.False(app.Model.Setup.IsUsable);

            shell.SetDropTargeted(true);
            Assert.False(shell.CanAcceptDrop);
            Assert.False(shell.ShowsDropHint);

            await shell.DropAsync([Dropped(directory, "office.ovpn")]);

            Assert.DoesNotContain(nameof(IDaemonApi.ImportProfileAsync), app.Calls.Calls);
            Assert.Null(app.Model.ImportReport);
            Assert.Empty(app.Store.Profiles);
        });
    }

    [Fact]
    public async Task TheOutlineFollowsTheHelperComingAndGoingWhileTheDragIsOverTheWindow()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.Running });
        await app.RunAsync(async () =>
        {
            using var shell = NewShell(app);
            var changed = new List<string?>();
            shell.PropertyChanged += (_, args) => changed.Add(args.PropertyName);
            shell.SetDropTargeted(true);
            Assert.True(shell.ShowsDropHint);

            app.Daemon.Kill();
            await Wait.UntilAsync("the outage", () => !app.Model.Setup.IsUsable);

            Assert.False(shell.ShowsDropHint);
            Assert.Contains(nameof(ShellViewModel.CanAcceptDrop), changed);
            Assert.Contains(nameof(ShellViewModel.ShowsDropHint), changed);
        });
    }

    [Fact]
    public void ItemsAreSortedByWhetherTheyAreFilesNotByTheirNames()
    {
        var text = TestText.En;
        DroppedItem[] items =
        [
            new("a.txt", @"C:\drop\a.txt"),
            new("b", @"C:\drop\b"),
            new("A.TXT", @"C:\DROP\A.TXT"),
            new("c.zip\\x", null),
            new("d", string.Empty),
        ];

        var batch = DropBatch.Of(items, text);

        Assert.Equal([@"C:\drop\a.txt", @"C:\drop\b"], batch.Files);
        Assert.Equal(["c.zip\\x", "d"], batch.Ignored.Select(outcome => outcome.Filename));
        Assert.All(batch.Ignored, outcome => Assert.Equal(text.NotAFileOnThisPC, Assert.IsType<ImportOutcome.Failed>(outcome).Message));
        Assert.NotEqual(text.NotAFileOnThisPC, TestText.Zh.NotAFileOnThisPC);
    }
}
