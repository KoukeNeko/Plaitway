using System.Globalization;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.Presentation;

/// <summary>
/// Everything the Diagnostics page knows, as plain text for a bug report. No log lines: they are the person's to
/// review before they share them. The headings are English whatever the language of the app, because the people who
/// read a report do not choose it.
/// </summary>
public static class DiagnosticsReport
{
    private const int RecentChangesInReport = 20;
    private const string Absent = "-";

    /// <summary>The report.</summary>
    /// <param name="diagnostics">What the daemon reported.</param>
    /// <param name="appVersion">The version of this app, when known.</param>
    /// <param name="daemon">What the daemon said about itself, when known.</param>
    /// <param name="text">The strings, for the states of routes.</param>
    /// <param name="profileName">Maps a profile id to its name; null for one that is gone.</param>
    public static string Text(Diagnostics diagnostics, string? appVersion, DaemonInfo? daemon, UiText text, Func<string, string?> profileName)
    {
        List<string> lines = [$"Plaitway {appVersion ?? Absent}", .. DaemonLines(daemon)];
        AppendNetwork(lines, diagnostics.Network ?? new NetworkInfo());
        AppendSection(lines, "Owned routes", diagnostics.OwnedRoutes.Select(route => OwnedRouteLine(route, text, profileName)));
        AppendSection(lines, "Stale routes", diagnostics.StaleRoutes.Select(StaleRouteLine));
        AppendSection(lines, "Resolver entries", diagnostics.ResolverEntries.Select(entry => $"  {entry}"));
        AppendSection(lines, "Recent changes", diagnostics.RecentJournal.TakeLast(RecentChangesInReport).Select(entry => JournalLine(entry, profileName)));
        return string.Join("\n", lines);
    }

    private static IEnumerable<string> DaemonLines(DaemonInfo? daemon)
    {
        if (daemon is null)
        {
            yield break;
        }

        yield return $"Helper {daemon.Version} (privileged: {(daemon.Privileged ? "true" : "false")})";
        foreach (var engine in daemon.Engines)
        {
            yield return $"  {EngineName(engine.Kind)}: {(engine.Available ? engine.Version : engine.Detail)}";
        }
    }

    /// <summary>The name of an engine, which is the name of its protocol.</summary>
    public static string EngineName(ProfileKind kind) => kind == ProfileKind.Wireguard ? "WireGuard" : "OpenVPN";

    private static void AppendNetwork(List<string> lines, NetworkInfo network)
    {
        lines.Add(string.Empty);
        lines.Add("Network");
        lines.Add($"  gateway IPv4: {network.DefaultGatewayV4} {network.DefaultInterfaceV4}");
        lines.Add($"  gateway IPv6: {network.DefaultGatewayV6} {network.DefaultInterfaceV6}");
        lines.Add($"  interfaces: {string.Join(", ", network.Interfaces)}");
        if (network.LastChange is not null)
        {
            lines.Add($"  last change: {Iso(network.LastChange.ToDateTimeOffset())} {network.LastChangeReason}");
        }
    }

    private static void AppendSection(List<string> lines, string heading, IEnumerable<string> entries)
    {
        lines.Add(string.Empty);
        lines.Add(heading);
        lines.AddRange(entries);
    }

    private static string OwnedRouteLine(OwnedRoute route, UiText text, Func<string, string?> profileName) =>
        $"  {route.Prefix} {route.State.Label(text)} {route.Kind.Label(text)} via {route.Via} ({profileName(route.Owner) ?? route.Owner})";

    private static string StaleRouteLine(StaleRoute route) =>
        $"  {route.Prefix} via {route.Gateway} {route.Interface}: {route.Reason}{(route.Owned ? " (installed by Plaitway)" : string.Empty)}";

    private static string JournalLine(JournalEntry entry, Func<string, string?> profileName)
    {
        var time = entry.Time is null ? Absent : Iso(entry.Time.ToDateTimeOffset());
        var owner = entry.Owner.Length == 0 ? string.Empty : profileName(entry.Owner) ?? entry.Owner;
        return $"  {time} {entry.Kind} {entry.Key} {entry.State} {owner}";
    }

    private static string Iso(DateTimeOffset time) => time.ToString("yyyy-MM-dd'T'HH:mm:sszzz", CultureInfo.InvariantCulture);
}
