using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// macos/Tests/PlaitwayTests/App/ModelTests.swift, DaemonSetupTests and AggregateStateTests. The states of the macOS helper
/// (a login item that needs approval, an app outside Applications) have no Windows counterpart; the Windows ones are the
/// service's: not installed, stopped, running without an answer.
/// </summary>
public sealed class DaemonSetupTests
{
    private const string AppVersion = "0.1.0";

    private static DaemonSetup Resolve(
        DaemonConnection connection,
        HelperState helper,
        bool hasExecutable = true,
        string? daemonVersion = null,
        string? appVersion = AppVersion,
        bool isOverridden = false,
        bool isSettling = false,
        DaemonFailureKind? cause = null) =>
        DaemonSetup.From(new SetupInputs(
            connection, cause, new HelperStatus(helper, hasExecutable ? @"C:\Plaitway\plaitwayd.exe" : null), daemonVersion, appVersion, isOverridden, isSettling));

    [Theory]
    [InlineData(DaemonConnection.Connected, HelperState.Running, true, "0.1.0", AppVersion, false, false, SetupKind.Ready)]
    [InlineData(DaemonConnection.Connected, HelperState.Running, true, "0.0.9", AppVersion, false, false, SetupKind.VersionMismatch)]
    [InlineData(DaemonConnection.Connected, HelperState.Running, true, "0.0.0-dev", null, false, false, SetupKind.Ready)]
    [InlineData(DaemonConnection.Connected, HelperState.Running, true, "0.0.0-dev", AppVersion, true, false, SetupKind.Ready)]
    [InlineData(DaemonConnection.Connected, HelperState.NotInstalled, true, null, AppVersion, false, false, SetupKind.Ready)]
    [InlineData(DaemonConnection.Unavailable, HelperState.NotInstalled, true, null, AppVersion, false, false, SetupKind.NeedsInstall)]

    // A helper that something else installed may be about to answer: not "not installed" yet.
    [InlineData(DaemonConnection.Connecting, HelperState.NotInstalled, true, null, AppVersion, false, false, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Unavailable, HelperState.NotInstalled, false, null, AppVersion, false, false, SetupKind.HelperMissing)]
    [InlineData(DaemonConnection.Connecting, HelperState.NotInstalled, false, null, AppVersion, false, false, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Connected, HelperState.NotInstalled, false, "0.1.0", AppVersion, false, false, SetupKind.Ready)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Stopped, true, null, AppVersion, false, false, SetupKind.Stopped)]
    [InlineData(DaemonConnection.Connecting, HelperState.Stopped, true, null, AppVersion, false, false, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Stopped, true, null, AppVersion, false, true, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Running, true, null, AppVersion, false, false, SetupKind.NotResponding)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Running, true, null, AppVersion, false, true, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Connecting, HelperState.Running, true, null, AppVersion, false, false, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Starting, true, null, AppVersion, false, false, SetupKind.Connecting)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Stopping, true, null, AppVersion, false, false, SetupKind.NotResponding)]
    [InlineData(DaemonConnection.Unavailable, HelperState.Unknown, true, null, AppVersion, false, false, SetupKind.NotResponding)]
    [InlineData(DaemonConnection.Unavailable, HelperState.NotInstalled, true, null, AppVersion, true, false, SetupKind.Development)]
    [InlineData(DaemonConnection.Connecting, HelperState.NotInstalled, true, null, AppVersion, true, false, SetupKind.Connecting)]
    public void Resolves(
        DaemonConnection connection, HelperState helper, bool hasExecutable, string? daemonVersion, string? appVersion, bool isOverridden, bool isSettling, SetupKind expected)
    {
        var setup = Resolve(connection, helper, hasExecutable, daemonVersion, appVersion, isOverridden, isSettling);

        Assert.Equal(expected, setup.Kind);
    }

    [Fact]
    public void AMismatchNamesBothVersions()
    {
        var setup = Resolve(DaemonConnection.Connected, HelperState.Running, daemonVersion: "0.0.9");

        Assert.Equal(DaemonSetup.VersionMismatch("0.0.9", AppVersion), setup);
    }

    [Theory]
    [InlineData(DaemonFailureKind.PermissionDenied)]
    [InlineData(DaemonFailureKind.ServerRefused)]
    [InlineData(DaemonFailureKind.Unavailable)]
    public void ARunningServiceThatIsNotAnsweringKeepsTheReasonTheWatchGave(DaemonFailureKind cause)
    {
        var setup = Resolve(DaemonConnection.Unavailable, HelperState.Running, cause: cause);

        Assert.Equal(DaemonSetup.NotResponding(cause), setup);
    }

    [Fact]
    public void OnlyAnAnsweringDaemonIsUsable()
    {
        Assert.True(DaemonSetup.Ready.IsUsable);
        Assert.True(DaemonSetup.VersionMismatch("1", "2").IsUsable);
        foreach (var kind in Enum.GetValues<SetupKind>().Except([SetupKind.Ready, SetupKind.VersionMismatch]))
        {
            Assert.False(new DaemonSetup(kind).IsUsable, kind.ToString());
        }
    }

    [Fact]
    public void OneThingToDoPerState()
    {
        var text = TestText.En;

        Assert.Equal(SetupAction.InstallHelper, new DaemonSetup(SetupKind.NeedsInstall).Content(text)?.Primary);
        Assert.Equal(SetupAction.StartHelper, new DaemonSetup(SetupKind.Stopped).Content(text)?.Primary);
        Assert.Equal(SetupAction.Retry, DaemonSetup.NotResponding().Content(text)?.Primary);
        Assert.Equal(SetupAction.ReinstallHelper, DaemonSetup.NotResponding().Content(text)?.Secondary);
        Assert.Equal(SetupAction.Retry, new DaemonSetup(SetupKind.HelperMissing).Content(text)?.Primary);
        Assert.Equal(SetupAction.Retry, new DaemonSetup(SetupKind.Development).Content(text)?.Primary);
        Assert.Null(DaemonSetup.Connecting.Content(text)?.Primary);
        Assert.True(DaemonSetup.Connecting.Content(text)?.ShowsProgress);

        // The profiles are shown once the daemon answers, even when it is out of date.
        Assert.Null(DaemonSetup.Ready.Content(text));
        Assert.Null(DaemonSetup.VersionMismatch("1", "2").Content(text));
    }

    [Fact]
    public void RegisteringAgainIsOfferedOnlyWhereItCouldHelp()
    {
        var text = TestText.En;

        Assert.Equal(SetupAction.ReinstallHelper, DaemonSetup.NotResponding(DaemonFailureKind.Unavailable).Content(text)?.Secondary);
        Assert.Null(DaemonSetup.NotResponding(DaemonFailureKind.PermissionDenied).Content(text)?.Secondary);
        Assert.Null(DaemonSetup.NotResponding(DaemonFailureKind.ServerRefused).Content(text)?.Secondary);
    }

    [Fact]
    public void AnUnansweredHelperSaysWhereToLook()
    {
        var detail = DaemonSetup.NotResponding().Content(TestText.En)?.Detail;

        Assert.Contains(HelperPaths.LogPath, detail, StringComparison.Ordinal);
    }

    [Fact]
    public void AnAccountThatMayNotUseTheHelperIsToldSo()
    {
        var denied = DaemonSetup.NotResponding(DaemonFailureKind.PermissionDenied).Content(TestText.En)?.Detail;
        var refused = DaemonSetup.NotResponding(DaemonFailureKind.ServerRefused).Content(TestText.En)?.Detail;

        Assert.Equal(TestText.En.ThisAccountMayNotUseTheHelper, denied);
        Assert.Equal(TestText.En.TheHelperSPipeBelongsToAnUnexpectedAccountPlaitwayDoesNotUseIt, refused);
    }

    [Fact]
    public void OnlyAMissingHelperOpensTheWindowAtLaunch()
    {
        foreach (var kind in new[] { SetupKind.NeedsInstall, SetupKind.Stopped })
        {
            Assert.True(new DaemonSetup(kind).OffersInstallation, kind.ToString());
        }

        // A helper that answers, however it was installed, needs no window.
        foreach (var kind in Enum.GetValues<SetupKind>().Except([SetupKind.NeedsInstall, SetupKind.Stopped]))
        {
            Assert.False(new DaemonSetup(kind).OffersInstallation, kind.ToString());
        }
    }

    [Fact]
    public void EveryBlockedStateIsNamedInTheMenu()
    {
        Assert.Null(DaemonSetup.Ready.MenuStatus(TestText.En));
        foreach (var kind in Enum.GetValues<SetupKind>().Except([SetupKind.Ready]))
        {
            Assert.False(string.IsNullOrEmpty(new DaemonSetup(kind).MenuStatus(TestText.En)), kind.ToString());
        }
    }

    // AggregateStateTests

    public static TheoryData<string, AggregateState> Summaries => new()
    {
        { "", AggregateState.Idle },
        { "disconnected disconnected", AggregateState.Idle },
        { "disconnected connected", AggregateState.Connected },
        { "connected connecting", AggregateState.Connecting },
        { "connected reconnecting", AggregateState.Connecting },
        { "disconnected awaiting", AggregateState.NeedsCredentials },
        { "connecting awaiting", AggregateState.NeedsCredentials },
        { "connected awaiting", AggregateState.NeedsCredentials },
        { "failed awaiting", AggregateState.Problem },
        { "connected disconnecting", AggregateState.Connecting },
        { "connected failed", AggregateState.Problem },
        { "connecting failed", AggregateState.Problem },
    };

    [Theory]
    [MemberData(nameof(Summaries))]
    public void SummarisesTheProfiles(string states, AggregateState expected)
    {
        var profiles = states.Split(' ', StringSplitOptions.RemoveEmptyEntries)
            .Select((name, index) => Profiles.Make($"p{index}", state: StateNamed(name)))
            .ToList();

        Assert.Equal(expected, AggregateStates.Of(DaemonSetup.Ready, profiles));
        Assert.Equal(expected, AggregateStates.Of(DaemonSetup.VersionMismatch("1", "2"), profiles));
    }

    [Fact]
    public void ADaemonThatDoesNotAnswerHidesWhatTheProfilesSaid()
    {
        var profiles = new[] { Profiles.Make(state: ProfileState.Connected) };

        foreach (var kind in Enum.GetValues<SetupKind>().Except([SetupKind.Ready, SetupKind.VersionMismatch, SetupKind.Connecting]))
        {
            Assert.Equal(AggregateState.Unavailable, AggregateStates.Of(new DaemonSetup(kind), profiles));
        }

        Assert.Equal(AggregateState.Connecting, AggregateStates.Of(DaemonSetup.Connecting, profiles));
    }

    [Theory]
    [InlineData(AggregateState.Unavailable)]
    [InlineData(AggregateState.Idle)]
    [InlineData(AggregateState.Connected)]
    [InlineData(AggregateState.Connecting)]
    [InlineData(AggregateState.NeedsCredentials)]
    [InlineData(AggregateState.Problem)]
    public void HasAnIconAndALabelInBothLanguages(AggregateState state)
    {
        Assert.False(string.IsNullOrEmpty(state.Label(TestText.En)));
        Assert.False(string.IsNullOrEmpty(state.Label(TestText.Zh)));
        Assert.True(Enum.IsDefined(state.Icon()));
    }

    private static ProfileState StateNamed(string name) => name switch
    {
        "disconnected" => ProfileState.Disconnected,
        "connecting" => ProfileState.Connecting,
        "connected" => ProfileState.Connected,
        "disconnecting" => ProfileState.Disconnecting,
        "failed" => ProfileState.Failed,
        "reconnecting" => ProfileState.Reconnecting,
        "awaiting" => ProfileState.AwaitingCredentials,
        _ => throw new ArgumentOutOfRangeException(nameof(name), name, "not a state"),
    };
}
