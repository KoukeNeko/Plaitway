using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

// How the app shows a state. Every state has a shape of its own, a word and a colour, in that order of importance:
// the colour alone fails people who cannot tell green from red. The view maps an icon to a glyph and a tone to a
// theme brush; nothing here knows a font or a colour.

/// <summary>The shape of a state's glyph.</summary>
public enum StatusIcon
{
    /// <summary>A shield, outlined: nothing is connected.</summary>
    Idle,

    /// <summary>A shield with a check mark.</summary>
    Connected,

    /// <summary>A shield with an arrow circling.</summary>
    Connecting,

    /// <summary>Two arrows chasing each other.</summary>
    Reconnecting,

    /// <summary>An open lock.</summary>
    Disconnecting,

    /// <summary>A key.</summary>
    AwaitingCredentials,

    /// <summary>A shield with an exclamation mark.</summary>
    Failed,

    /// <summary>A warning triangle: the helper is not there.</summary>
    Unavailable,

    /// <summary>A check mark in a circle: the route is in effect.</summary>
    RouteInstalled,

    /// <summary>A clock.</summary>
    RoutePending,

    /// <summary>A minus in a circle: another profile has the prefix.</summary>
    RouteShadowed,

    /// <summary>A warning triangle: a local network is in the way.</summary>
    RouteBlocked,

    /// <summary>A cross in a circle.</summary>
    RouteFailed,

    /// <summary>An empty circle: the state is not known.</summary>
    RouteUnknown,
}

/// <summary>The colour family of a state; the view picks the theme's brush for it.</summary>
public enum StatusTone
{
    /// <summary>Secondary text colour: at rest, or standing by.</summary>
    Neutral,

    /// <summary>Green: it works.</summary>
    Success,

    /// <summary>Amber: under way, or waiting for the user.</summary>
    Caution,

    /// <summary>Red: it failed.</summary>
    Critical,
}

/// <summary>The icons, tones and motion of the states.</summary>
public static class StatusVisuals
{
    /// <summary>The glyph of a profile in this state; the same family in the sidebar, on the page and in the tray.</summary>
    public static StatusIcon Icon(this ProfileState state) => state switch
    {
        ProfileState.Connected => StatusIcon.Connected,
        ProfileState.Connecting => StatusIcon.Connecting,
        ProfileState.Reconnecting => StatusIcon.Reconnecting,
        ProfileState.Disconnecting => StatusIcon.Disconnecting,
        ProfileState.AwaitingCredentials => StatusIcon.AwaitingCredentials,
        ProfileState.Failed => StatusIcon.Failed,
        _ => StatusIcon.Idle,
    };

    /// <summary>The colour of a profile in this state.</summary>
    public static StatusTone Tone(this ProfileState state) => state switch
    {
        ProfileState.Connected => StatusTone.Success,
        ProfileState.Connecting or ProfileState.Reconnecting or ProfileState.AwaitingCredentials or ProfileState.Disconnecting => StatusTone.Caution,
        ProfileState.Failed => StatusTone.Critical,
        _ => StatusTone.Neutral,
    };

    /// <summary>Something is going on that ends by itself: the glyph may move.</summary>
    public static bool IsTransitional(this ProfileState state) =>
        state is ProfileState.Connecting or ProfileState.Reconnecting or ProfileState.Disconnecting;

    /// <summary>The glyph of a route in this state.</summary>
    public static StatusIcon Icon(this RouteState state) => state switch
    {
        RouteState.Installed => StatusIcon.RouteInstalled,
        RouteState.Pending => StatusIcon.RoutePending,
        RouteState.Shadowed => StatusIcon.RouteShadowed,
        RouteState.Blocked => StatusIcon.RouteBlocked,
        RouteState.Failed => StatusIcon.RouteFailed,
        _ => StatusIcon.RouteUnknown,
    };

    /// <summary>
    /// A shadowed prefix is a profile standing by behind a higher priority one, which is how it is meant to work:
    /// neutral. A blocked one is a conflict the user may want to know about.
    /// </summary>
    public static StatusTone Tone(this RouteState state) => state switch
    {
        RouteState.Installed => StatusTone.Success,
        RouteState.Pending or RouteState.Blocked => StatusTone.Caution,
        RouteState.Failed => StatusTone.Critical,
        _ => StatusTone.Neutral,
    };

    /// <summary>The prefix is not in the routing table although the profile asks for it.</summary>
    public static bool IsLost(this RouteState state) => state is RouteState.Blocked or RouteState.Failed;
}
