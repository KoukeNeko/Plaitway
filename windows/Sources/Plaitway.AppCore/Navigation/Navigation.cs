namespace Plaitway.AppCore.Navigation;

/// <summary>What the sidebar selects: a profile, Diagnostics or the app's Settings.</summary>
public abstract record SidebarItem
{
    private protected SidebarItem()
    {
    }

    /// <summary>Diagnostics.</summary>
    public static SidebarItem Diagnostics { get; } = new DiagnosticsItem();

    /// <summary>The app's own settings and the helper.</summary>
    public static SidebarItem Settings { get; } = new SettingsItem();

    /// <summary>A profile.</summary>
    public static SidebarItem Profile(string id) => new ProfileItem(id);

    /// <summary>A profile of the sidebar.</summary>
    /// <param name="Id">The profile's id.</param>
    public sealed record ProfileItem(string Id) : SidebarItem;

    /// <summary>The Diagnostics entry.</summary>
    public sealed record DiagnosticsItem : SidebarItem;

    /// <summary>The Settings entry.</summary>
    public sealed record SettingsItem : SidebarItem;
}

/// <summary>The five pages of a profile, in the order of its tabs.</summary>
public enum ProfileSection
{
    /// <summary>State, cause, traffic, endpoints.</summary>
    Overview,

    /// <summary>What the profile put in the routing table and in DNS.</summary>
    Routes,

    /// <summary>The live log.</summary>
    Logs,

    /// <summary>The profile's text.</summary>
    Configuration,

    /// <summary>Name, priority, tunnel mode, on-demand rules.</summary>
    Settings,
}

/// <summary>The pages of Diagnostics.</summary>
public enum DiagnosticsPage
{
    /// <summary>The network, the routes the daemon owns, the resolver entries, the recent changes, the stale routes.</summary>
    Overview,

    /// <summary>The daemon's own log.</summary>
    DaemonLog,
}
