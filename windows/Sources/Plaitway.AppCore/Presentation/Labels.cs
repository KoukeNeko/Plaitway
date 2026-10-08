using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>The words for the states and kinds of the daemon's API; the wording of the macOS app.</summary>
public static class Labels
{
    private const string OpenVpnName = "OpenVPN";
    private const string WireGuardName = "WireGuard";
    private const string EveryDomainMarker = ".";

    /// <summary>The state of a profile in a word.</summary>
    public static string Label(this ProfileState state, UiText text) => state switch
    {
        ProfileState.Connected => text.Connected,
        ProfileState.Connecting => text.Connecting,
        ProfileState.Disconnecting => text.Disconnecting,
        ProfileState.Disconnected => text.Disconnected,
        ProfileState.Failed => text.Failed,
        ProfileState.Reconnecting => text.Reconnecting,
        ProfileState.AwaitingCredentials => text.AwaitingCredentials,
        _ => text.Unknown,
    };

    /// <summary>Protocol names are product names and stay untranslated.</summary>
    public static string Label(this ProfileKind kind, UiText text) => kind switch
    {
        ProfileKind.Openvpn => OpenVpnName,
        ProfileKind.Wireguard => WireGuardName,
        _ => text.Unknown,
    };

    /// <summary>The tunnel modes a profile can be set to, in the order the picker lists them.</summary>
    public static IReadOnlyList<TunnelMode> TunnelModeChoices { get; } = [TunnelMode.Auto, TunnelMode.Full, TunnelMode.Split];

    /// <summary>The tunnel mode in words; an unspecified mode is the automatic one.</summary>
    public static string Label(this TunnelMode mode, UiText text) => mode switch
    {
        TunnelMode.Full => text.FullTunnel,
        TunnelMode.Split => text.SplitTunnel,
        _ => text.Auto,
    };

    /// <summary>The state of a route in words.</summary>
    /// <param name="state">The state.</param>
    /// <param name="text">The strings.</param>
    /// <param name="shadowedBy">The name of the profile that holds the prefix instead, when there is one.</param>
    public static string Label(this RouteState state, UiText text, string? shadowedBy = null) => state switch
    {
        RouteState.Installed => text.Installed,
        RouteState.Pending => text.Pending,
        RouteState.Shadowed => shadowedBy is null ? text.Shadowed : text.ShadowedBy(shadowedBy),
        RouteState.Blocked => text.BlockedByLocalNetwork,
        RouteState.Failed => text.Failed,
        _ => text.Unknown,
    };

    /// <summary>What a route is for, in words.</summary>
    public static string Label(this RouteKind kind, UiText text) => kind switch
    {
        RouteKind.Tunnel => text.Tunnel,
        RouteKind.Bypass => text.Bypass,
        RouteKind.Default => text.DefaultRoute,
        _ => text.Unknown,
    };

    /// <summary>The name of a log level; the same in every language.</summary>
    public static string Tag(this LogLevel level) => level switch
    {
        LogLevel.Debug => "DEBUG",
        LogLevel.Warn => "WARN",
        LogLevel.Error => "ERROR",
        _ => "INFO",
    };

    /// <summary>The colour family of a log line; errors and warnings stand out, debug lines recede.</summary>
    public static StatusTone Tone(this LogLevel level) => level switch
    {
        LogLevel.Warn => StatusTone.Caution,
        LogLevel.Error => StatusTone.Critical,
        _ => StatusTone.Neutral,
    };

    /// <summary>The domains of a DNS entry; the daemon's dot means every domain.</summary>
    public static string DomainLabel(string domain, UiText text) => domain == EveryDomainMarker ? text.AllDomains : domain;

    /// <summary>"Line 4: up – removed, a profile cannot run programs".</summary>
    public static string Summary(this ImportWarning warning, UiText text)
    {
        var place = warning.Line > 0 ? text.Line(warning.Line) + ": " : string.Empty;
        var detail = string.Join(" – ", new[] { warning.Directive, warning.Message }.Where(part => part.Length > 0));
        return place + detail;
    }
}
