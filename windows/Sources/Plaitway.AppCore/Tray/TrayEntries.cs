using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;

namespace Plaitway.AppCore.Tray;

/// <summary>What choosing an entry of the tray menu does.</summary>
public enum TrayCommand
{
    /// <summary>Nothing: a line of text or a separator.</summary>
    None,

    /// <summary>A profile: connects, disconnects or retries it, or asks for its credentials.</summary>
    ChooseProfile,

    /// <summary>Switches every profile off.</summary>
    DisconnectAll,

    /// <summary>Shows the window.</summary>
    Open,

    /// <summary>Asks for profile files.</summary>
    Import,

    /// <summary>Opens the app's settings.</summary>
    Settings,

    /// <summary>Quits.</summary>
    Quit,
}

/// <summary>An entry of the tray menu, before it is a native menu item.</summary>
/// <param name="Command">What choosing it does.</param>
/// <param name="Text">What it says.</param>
/// <param name="Detail">A second column on the right: what a profile is and how it stands.</param>
/// <param name="ProfileId">For <see cref="TrayCommand.ChooseProfile"/>, the profile.</param>
/// <param name="IsEnabled">It can be chosen.</param>
/// <param name="Mark">The check mark or the dot of a profile.</param>
/// <param name="IsDefault">The entry a click on the icon does; drawn bold.</param>
/// <param name="IsSeparator">A line between groups.</param>
public sealed record TrayEntry(
    TrayCommand Command,
    string Text,
    string? Detail = null,
    string? ProfileId = null,
    bool IsEnabled = true,
    MenuMark Mark = MenuMark.Off,
    bool IsDefault = false,
    bool IsSeparator = false)
{
    /// <summary>A line between groups.</summary>
    public static TrayEntry Separator { get; } = new(TrayCommand.None, string.Empty, IsEnabled: false, IsSeparator: true);
}

/// <summary>The tray menu: how many are connected, one row per profile, Disconnect All, then Open, Import, Settings and Quit.</summary>
public static class TrayMenuBuilder
{
    /// <summary>The entries for <paramref name="menu"/>.</summary>
    public static IReadOnlyList<TrayEntry> Build(MenuModel menu, UiText text)
    {
        List<TrayEntry> entries = [];
        if (menu.Status is { } status)
        {
            entries.Add(new TrayEntry(TrayCommand.None, status, IsEnabled: false));
        }

        if (menu.Summary is { } summary)
        {
            entries.Add(new TrayEntry(TrayCommand.None, summary, IsEnabled: false));
        }

        entries.AddRange(menu.Items.Select(item => new TrayEntry(TrayCommand.ChooseProfile, item.Title, item.Subtitle, item.ProfileId, item.IsEnabled, item.Mark)));
        if (menu.CanDisconnectAll)
        {
            entries.Add(TrayEntry.Separator);
            entries.Add(new TrayEntry(TrayCommand.DisconnectAll, text.DisconnectAll));
        }

        if (entries.Count > 0)
        {
            entries.Add(TrayEntry.Separator);
        }

        entries.Add(new TrayEntry(TrayCommand.Open, text.OpenPlaitway, IsDefault: true));
        entries.Add(new TrayEntry(TrayCommand.Import, text.ImportProfileEllipsis, IsEnabled: menu.CanImport));
        entries.Add(new TrayEntry(TrayCommand.Settings, text.SettingsEllipsis));
        entries.Add(TrayEntry.Separator);
        entries.Add(new TrayEntry(TrayCommand.Quit, text.QuitPlaitway));
        return entries;
    }
}
