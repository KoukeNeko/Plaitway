using Microsoft.Extensions.Logging.Abstractions;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Logs;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.ViewModels;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// A log is followed by a new stream every time its page is opened, and the daemon starts every stream with the
/// buffered tail again. macos/Sources/PlaitwayMenuBar/LogTail.swift replaces what is shown when a stream's first line
/// comes; these keep the Windows tail from adding the tail to itself.
/// </summary>
public sealed class LogStreamTests(DaemonBinary binary)
{
    private static readonly string[] TheTail = ["first", "second", "third"];

    private static ScriptedLogApi DaemonWithTail(params string[] tail) => new(_ => [.. tail.Select(text => ScriptedLogApi.Line(text))]);

    private static ProfileStore StoreOf(ScriptedLogApi api, UiThread ui) =>
        new(api, new InMemoryCredentialStore(), ui, NullLogger<ProfileStore>.Instance);

    private static async Task FollowOnceAsync(LogTail tail, ProfileStore store, Func<bool> shownAgain)
    {
        using var stop = new CancellationTokenSource();
        var following = tail.RunAsync(store, string.Empty, stop.Token);
        await Wait.UntilAsync("the stream's lines to be shown", shownAgain);
        await stop.CancelAsync();
        await following;
    }

    [Fact]
    public async Task OpeningTheLogAgainShowsTheBufferedTailOnceAndNotAgainOnTopOfItself()
    {
        using var ui = new UiThread();
        var tail = new LogTail(TimeProvider.System);
        var store = StoreOf(DaemonWithTail(TheTail), ui);

        // Three visits: the lines shown stay three, not three, six and nine.
        for (var visit = 1; visit <= 3; visit++)
        {
            var firstIdOfVisit = (visit - 1) * TheTail.Length;
            await FollowOnceAsync(tail, store, () => tail.Entries.Any(entry => entry.Id >= firstIdOfVisit));

            Assert.Equal(TheTail, tail.Entries.Select(entry => entry.Text));
            Assert.All(tail.Entries, entry => Assert.True(entry.Id >= firstIdOfVisit, $"visit {visit} still shows a line of an earlier stream"));
        }
    }

    [Fact]
    public async Task AStreamWithNoTailClearsWhatTheOldStreamShowed()
    {
        using var ui = new UiThread();
        var tail = new LogTail(TimeProvider.System);
        var daemon = new ScriptedLogApi(stream => stream == 1 ? [ScriptedLogApi.Line("old one"), ScriptedLogApi.Line("old two")] : []);
        var store = StoreOf(daemon, ui);

        await FollowOnceAsync(tail, store, () => tail.Entries.Count == 2);

        // The log was emptied on the daemon's side: the second stream brings nothing, and the old lines are not its lines.
        await FollowOnceAsync(tail, store, () => tail.Entries.Count == 0);

        Assert.Empty(tail.Entries);
        Assert.Equal(2, daemon.StreamsOpened);
    }

    [Fact]
    public async Task LinesLoggedWhileTheStreamIsOpenAreAppendedToTheTail()
    {
        using var ui = new UiThread();
        var tail = new LogTail(TimeProvider.System);
        var daemon = DaemonWithTail(TheTail);
        using var stop = new CancellationTokenSource();

        var following = tail.RunAsync(StoreOf(daemon, ui), string.Empty, stop.Token);
        await Wait.UntilAsync("the tail", () => tail.Entries.Count == TheTail.Length);
        daemon.Push(ScriptedLogApi.Line("live one"));
        await Wait.UntilAsync("the live line", () => tail.Entries.Count == TheTail.Length + 1);
        daemon.Push(ScriptedLogApi.Line("live two"));
        await Wait.UntilAsync("the second live line", () => tail.Entries.Count == TheTail.Length + 2);
        await stop.CancelAsync();
        await following;

        Assert.Equal([.. TheTail, "live one", "live two"], tail.Entries.Select(entry => entry.Text));
    }

    [Fact]
    public async Task AStreamThatIsLeftBeforeItSaysAnythingKeepsTheLinesForTheNextStreamToReplace()
    {
        using var ui = new UiThread();
        var tail = new LogTail(TimeProvider.System);
        var daemon = new ScriptedLogApi(stream => stream == 1 ? [ScriptedLogApi.Line("kept")] : []);
        var store = StoreOf(daemon, ui);
        await FollowOnceAsync(tail, store, () => tail.Entries.Count == 1);

        // The page is left again before the grace period for the second stream's tail has passed.
        using var stop = new CancellationTokenSource();
        var following = tail.RunAsync(store, string.Empty, stop.Token);
        await stop.CancelAsync();
        await following;

        Assert.Equal(["kept"], tail.Entries.Select(entry => entry.Text));
    }

    [Fact]
    public async Task LeavingTheLogsTabAndComingBackShowsTheSameLines()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("office.ovpn");
            await app.Store.SetEnabledAsync(id, enabled: true);
            await app.WaitForStateAsync(id, ProfileState.Connected);
            using var logs = new LogsViewModel(app.Model, id, new FakeClipboard());
            logs.SelectedChoice = logs.Choices[(int)LogFilter.All];

            logs.Activate();
            await Wait.UntilAsync("the tail", () => logs.Lines.Any(line => line.Text.StartsWith("connected on", StringComparison.Ordinal)));
            var firstVisit = logs.Lines.Select(line => line.Text).ToList();
            var idsOfFirstVisit = logs.Lines.Count;

            for (var visit = 0; visit < 2; visit++)
            {
                logs.Deactivate();
                await logs.StoppedAsync();
                logs.Activate();
                await Wait.UntilAsync("the new stream's tail", () => logs.Lines.Any(line => line.Id >= idsOfFirstVisit * (visit + 1)));

                Assert.Equal(firstVisit, logs.Lines.Select(line => line.Text));
            }

            logs.Deactivate();
            await logs.StoppedAsync();
        });
    }

    [Fact]
    public async Task ADaemonThatRestartsInsideOneStreamDoesNotRepeatTheLinesAlreadyShown()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var logs = new LogsViewModel(app.Model, string.Empty, new FakeClipboard());
            logs.SelectedChoice = logs.Choices[(int)LogFilter.All];
            logs.Activate();
            await Wait.UntilAsync("the daemon's own log", () => logs.Lines.Count > 0);
            var before = logs.Lines.Select(line => line.PlainText).ToList();
            var streamsBefore = app.Calls.Calls.Count(call => call == nameof(IDaemonApi.WatchLogsAsync));

            app.Daemon.Kill();
            await app.Daemon.StartAsync();
            await Wait.UntilAsync("the restarted daemon's first lines", () => logs.Lines.Count > before.Count);

            // The client reconnected inside the stream and skipped the lines it had delivered: nothing is shown twice, and
            // what was shown stays.
            Assert.Equal(streamsBefore, app.Calls.Calls.Count(call => call == nameof(IDaemonApi.WatchLogsAsync)));
            Assert.Equal(before, logs.Lines.Take(before.Count).Select(line => line.PlainText));
            Assert.Equal(logs.Lines.Count, logs.Lines.Select(line => (line.Time, line.Level, line.Text)).Distinct().Count());

            logs.Deactivate();
            await logs.StoppedAsync();
        });
    }

    [Fact]
    public async Task TheHelperLogOfDiagnosticsOpensOneStreamPerVisit()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            using var diagnostics = new DiagnosticsViewModel(app.Model, new FakeClipboard(), new FakeDialogs());
            int StreamsOpened() => app.Calls.Calls.Count(call => call == nameof(IDaemonApi.WatchLogsAsync));
            diagnostics.HelperLog.SelectedChoice = diagnostics.HelperLog.Choices[(int)LogFilter.All];

            app.Model.DiagnosticsPage = DiagnosticsPage.Overview;
            diagnostics.Activate();
            Assert.Equal(0, StreamsOpened());

            app.Model.DiagnosticsPage = DiagnosticsPage.DaemonLog;
            Assert.Equal(1, StreamsOpened());
            await Wait.UntilAsync("the helper log", () => diagnostics.HelperLog.Lines.Count > 0);
            var shown = diagnostics.HelperLog.Lines.Count;

            // The page is asked to be active again, and the tab is chosen again: no second stream for the same visit.
            diagnostics.Activate();
            app.Model.DiagnosticsPage = DiagnosticsPage.DaemonLog;
            Assert.Equal(1, StreamsOpened());

            app.Model.DiagnosticsPage = DiagnosticsPage.Overview;
            app.Model.DiagnosticsPage = DiagnosticsPage.DaemonLog;
            Assert.Equal(2, StreamsOpened());
            await Wait.UntilAsync("the new stream's tail", () => diagnostics.HelperLog.Lines.Any(line => line.Id >= shown));
            Assert.All(diagnostics.HelperLog.Lines, line => Assert.True(line.Id >= shown, "a line of the first visit is still shown"));

            diagnostics.Deactivate();
            await diagnostics.HelperLog.StoppedAsync();
        });
    }
}
