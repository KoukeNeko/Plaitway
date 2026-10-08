using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>What choosing a profile in the tray menu does: the menu has one row per profile and no buttons, so the row does what the profile needs.</summary>
public enum MenuAction
{
    /// <summary>Switch the profile on.</summary>
    Connect,

    /// <summary>Switch the profile off.</summary>
    Disconnect,

    /// <summary>A failed profile is switched on already; asking again retries.</summary>
    Retry,

    /// <summary>The credential dialog is in the window.</summary>
    AnswerCredentials,
}

/// <summary>How a row of the menu is marked: a check mark, a dot, or nothing.</summary>
public enum MenuMark
{
    /// <summary>Not connected.</summary>
    Off,

    /// <summary>Connected.</summary>
    On,

    /// <summary>Under way, or waiting for the user.</summary>
    Mixed,
}

/// <summary>A profile in the tray menu.</summary>
/// <param name="ProfileId">The profile.</param>
/// <param name="Title">The name.</param>
/// <param name="Subtitle">"OpenVPN · Connected": what it is and how it stands.</param>
/// <param name="Mark">How the row is marked.</param>
/// <param name="Action">What choosing the row does.</param>
/// <param name="IsEnabled">False while the profile is disconnecting: there is nothing to do.</param>
public sealed record MenuItemModel(string ProfileId, string Title, string Subtitle, MenuMark Mark, MenuAction Action, bool IsEnabled);

/// <summary>What the tray icon's menu lists, before it becomes native menu items.</summary>
public sealed record MenuModel
{
    /// <summary>Makes the menu for <paramref name="profiles"/> given where the helper stands.</summary>
    public MenuModel(DaemonSetup setup, IReadOnlyList<Profile> profiles, UiText text)
    {
        Status = setup.MenuStatus(text);
        CanImport = setup.IsUsable;

        // Profiles of a daemon that is not answering would show states that are no longer true.
        var shown = setup.IsUsable ? profiles : [];
        Items = [.. shown.Select(profile => ItemFor(profile, text))];
        CanDisconnectAll = shown.Any(profile => profile.DesiredEnabled);
        Summary = shown.Count == 0 ? null : SummaryOf(shown, setup, text);
    }

    /// <summary>Why the profiles below cannot be used, when they cannot.</summary>
    public string? Status { get; }

    /// <summary>The first line above the profiles: how the connections stand together.</summary>
    public string? Summary { get; }

    /// <summary>One row per profile, in priority order.</summary>
    public IReadOnlyList<MenuItemModel> Items { get; }

    /// <summary>Something is switched on.</summary>
    public bool CanDisconnectAll { get; }

    /// <summary>The daemon answers, so a profile can be imported.</summary>
    public bool CanImport { get; }

    /// <summary>Two menus are equal when they list the same rows, so that the icon redraws the menu only when it changed.</summary>
    public bool Equals(MenuModel? other) =>
        other is not null
        && Status == other.Status
        && Summary == other.Summary
        && CanDisconnectAll == other.CanDisconnectAll
        && CanImport == other.CanImport
        && Items.SequenceEqual(other.Items);

    /// <inheritdoc />
    public override int GetHashCode() => HashCode.Combine(Status, Summary, CanDisconnectAll, CanImport, Items.Count);

    /// <summary>The row of a profile.</summary>
    public static MenuItemModel ItemFor(Profile profile, UiText text)
    {
        var (mark, action) = profile.State switch
        {
            ProfileState.Connected => (MenuMark.On, MenuAction.Disconnect),
            ProfileState.Connecting or ProfileState.Reconnecting or ProfileState.Disconnecting => (MenuMark.Mixed, MenuAction.Disconnect),
            ProfileState.Failed => (MenuMark.Off, MenuAction.Retry),
            ProfileState.AwaitingCredentials => (MenuMark.Mixed, MenuAction.AnswerCredentials),
            _ => (MenuMark.Off, MenuAction.Connect),
        };
        return new MenuItemModel(
            profile.Id,
            profile.Name,
            $"{profile.Kind.Label(text)} · {profile.State.Label(text)}",
            mark,
            action,
            profile.State != ProfileState.Disconnecting);
    }

    private static string SummaryOf(IReadOnlyList<Profile> profiles, DaemonSetup setup, UiText text)
    {
        var aggregate = AggregateStates.Of(setup, profiles);
        var connected = profiles.Count(profile => profile.State == ProfileState.Connected);
        return aggregate == AggregateState.Connected || (connected > 0 && aggregate == AggregateState.Connecting)
            ? text.ConnectedColon(connected)
            : aggregate.Label(text);
    }
}
