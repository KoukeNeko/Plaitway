using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/App/ModelTests.swift, MenuModelTests.</summary>
public sealed class MenuModelTests
{
    private static readonly IReadOnlyList<Profile> Listed =
    [
        Profiles.Make("a", "Router", ProfileState.Connected, desired: true),
        Profiles.Make("b", "Home", ProfileState.Disconnected),
        Profiles.Make("c", "Office", ProfileState.Failed, desired: true),
        Profiles.Make("d", "Lab", ProfileState.Disconnecting),
        Profiles.Make("e", "Cafe", ProfileState.AwaitingCredentials, desired: true),
        Profiles.Make("f", "Depot", ProfileState.Reconnecting, desired: true),
    ];

    private static MenuModel Menu(DaemonSetup setup, IReadOnlyList<Profile>? profiles = null) => new(setup, profiles ?? Listed, TestText.En);

    [Fact]
    public void ListsTheProfilesInOrderWithTheirState()
    {
        var menu = Menu(DaemonSetup.Ready);

        Assert.Null(menu.Status);
        Assert.True(menu.CanImport);
        Assert.Equal(["Router", "Home", "Office", "Lab", "Cafe", "Depot"], menu.Items.Select(item => item.Title));
        Assert.Equal([MenuMark.On, MenuMark.Off, MenuMark.Off, MenuMark.Mixed, MenuMark.Mixed, MenuMark.Mixed], menu.Items.Select(item => item.Mark));
        Assert.Equal(
            new[] { ProfileState.Connected, ProfileState.Disconnected, ProfileState.Failed, ProfileState.Disconnecting, ProfileState.AwaitingCredentials, ProfileState.Reconnecting }
                .Select(state => $"{ProfileKind.Unspecified.Label(TestText.En)} · {state.Label(TestText.En)}"),
            menu.Items.Select(item => item.Subtitle));
        Assert.Equal([true, true, true, false, true, true], menu.Items.Select(item => item.IsEnabled));
        Assert.Equal(["a", "b", "c", "d", "e", "f"], menu.Items.Select(item => item.ProfileId));
    }

    [Fact]
    public void ARowDoesWhatItsProfileNeeds()
    {
        var menu = Menu(DaemonSetup.Ready);

        // Connected and under way: choosing it switches the profile off. A failed one is switched on already, so choosing
        // it tries again; one that waits for a password opens the dialog.
        Assert.Equal(
            [MenuAction.Disconnect, MenuAction.Connect, MenuAction.Retry, MenuAction.Disconnect, MenuAction.AnswerCredentials, MenuAction.Disconnect],
            menu.Items.Select(item => item.Action));
    }

    [Fact]
    public void SummarisesHowManyAreConnected()
    {
        var text = TestText.En;
        var connected = Menu(DaemonSetup.Ready, [Profiles.Make("a", state: ProfileState.Connected), Profiles.Make("b", state: ProfileState.Connected), Profiles.Make("c")]);
        Assert.Equal(text.ConnectedColon(2), connected.Summary);

        // A profile that is still connecting does not take the count away from the ones that are not.
        var mixed = Menu(DaemonSetup.Ready, [Profiles.Make("a", state: ProfileState.Connected), Profiles.Make("b", state: ProfileState.Connecting)]);
        Assert.Equal(text.ConnectedColon(1), mixed.Summary);

        Assert.Equal(AggregateState.Idle.Label(text), Menu(DaemonSetup.Ready, [Profiles.Make("a")]).Summary);
        Assert.Equal(AggregateState.Problem.Label(text), Menu(DaemonSetup.Ready, [Profiles.Make("a", state: ProfileState.Failed)]).Summary);
        Assert.Null(Menu(DaemonSetup.Ready, []).Summary);
    }

    [Fact]
    public void OffersDisconnectAllOnlyWhileSomethingIsSwitchedOn()
    {
        Assert.True(Menu(DaemonSetup.Ready).CanDisconnectAll);
        Assert.False(Menu(DaemonSetup.Ready, [Profiles.Make("a")]).CanDisconnectAll);
        Assert.False(Menu(DaemonSetup.NotResponding()).CanDisconnectAll);
    }

    [Fact]
    public void AnOutOfDateHelperStillWorksAndSaysSo()
    {
        var menu = Menu(DaemonSetup.VersionMismatch("0.0.9", "0.1.0"));

        Assert.Equal(DaemonSetup.VersionMismatch(string.Empty, string.Empty).MenuStatus(TestText.En), menu.Status);
        Assert.Equal(Listed.Count, menu.Items.Count);
        Assert.True(menu.CanImport);
    }

    [Theory]
    [InlineData(SetupKind.NeedsInstall)]
    [InlineData(SetupKind.Stopped)]
    [InlineData(SetupKind.HelperMissing)]
    [InlineData(SetupKind.NotResponding)]
    [InlineData(SetupKind.Development)]
    [InlineData(SetupKind.Connecting)]
    public void DisablesEverythingWhenTheHelperDoesNotAnswer(SetupKind kind)
    {
        var setup = new DaemonSetup(kind);
        var menu = Menu(setup);

        Assert.Equal(setup.MenuStatus(TestText.En), menu.Status);
        Assert.False(string.IsNullOrEmpty(menu.Status));
        Assert.Empty(menu.Items);
        Assert.False(menu.CanImport);
    }

    [Fact]
    public void TwoMenusThatListTheSameRowsAreEqualSoTheMenuIsNotRebuiltForANewByteCount()
    {
        var first = Menu(DaemonSetup.Ready);
        var counted = Listed.Select(profile => { var copy = profile.Clone(); copy.Status = new TunnelStatus { RxBytes = 99 }; return copy; }).ToList();

        Assert.Equal(first, Menu(DaemonSetup.Ready, counted));
        Assert.NotEqual(first, Menu(DaemonSetup.Ready, [.. Listed.Skip(1)]));
    }
}
