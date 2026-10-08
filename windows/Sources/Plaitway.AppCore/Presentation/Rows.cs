using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>
/// A row of the routes list: what the daemon installed for a profile, or, while it is not connected, the prefixes the
/// profile names itself.
/// </summary>
/// <param name="Id">The position in the list: the same prefix can be listed more than once.</param>
/// <param name="Prefix">The network prefix.</param>
/// <param name="State">Whether it is in effect; null for a prefix the profile only names.</param>
/// <param name="StateLabel">The state in words, naming the profile that holds a shadowed prefix; null without a state.</param>
/// <param name="Detail">The daemon's own explanation; empty when there is none.</param>
public sealed record RouteRow(int Id, string Prefix, RouteState? State, string? StateLabel, string Detail)
{
    /// <summary>What a screen reader says for the row: the prefix and, when it has one, its state.</summary>
    public string AutomationName => StateLabel is { Length: > 0 } label ? $"{Prefix}, {label}" : Prefix;

    /// <summary>A list item is named by the text of the item it shows.</summary>
    public override string ToString() => AutomationName;

    /// <summary>The rows for <paramref name="profile"/>.</summary>
    /// <param name="profile">The profile.</param>
    /// <param name="text">The strings.</param>
    /// <param name="profileName">Maps a profile id to the name shown for it; null for one that is gone.</param>
    public static IReadOnlyList<RouteRow> For(Profile profile, UiText text, Func<string, string?> profileName)
    {
        var routes = profile.Status?.Routes;
        if (routes is { Count: > 0 })
        {
            return [.. routes.Select((route, index) => new RouteRow(
                index,
                route.Prefix,
                route.State,
                route.State.Label(text, route.ShadowedBy.Length == 0 ? null : profileName(route.ShadowedBy) ?? route.ShadowedBy),
                route.Detail))];
        }

        return [.. (profile.Summary?.Routes ?? []).Select((prefix, index) => new RouteRow(index, prefix, null, null, string.Empty))];
    }
}

/// <summary>A DNS entry the daemon installed for a profile.</summary>
/// <param name="Id">The position in the list.</param>
/// <param name="Servers">The servers, comma separated.</param>
/// <param name="Domains">The domains it answers for, comma separated; "All domains" for the daemon's dot.</param>
/// <param name="State">Whether it is in effect.</param>
/// <param name="StateLabel">The state in words.</param>
/// <param name="Detail">The daemon's own explanation; empty when there is none.</param>
public sealed record DnsRow(int Id, string Servers, string Domains, RouteState State, string StateLabel, string Detail)
{
    /// <summary>The rows for <paramref name="profile"/>.</summary>
    public static IReadOnlyList<DnsRow> For(Profile profile, UiText text) =>
        [.. (profile.Status?.Dns ?? []).Select((dns, index) => new DnsRow(
            index,
            string.Join(", ", dns.Servers),
            string.Join(", ", dns.MatchDomains.Select(domain => Labels.DomainLabel(domain, text))),
            dns.State,
            dns.State.Label(text),
            dns.Detail))];
}
