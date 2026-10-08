using Plaitway.V1;

namespace Plaitway.Client;

/// <summary>A profile that was imported or edited, and what the daemon removed or ignored in it.</summary>
/// <param name="Profile">The profile with its refreshed summary.</param>
/// <param name="Warnings">Directives the daemon stripped or ignored.</param>
public sealed record ProfileImport(Profile Profile, IReadOnlyList<ImportWarning> Warnings);

/// <summary>Small conveniences on the generated types.</summary>
public static class ApiExtensions
{
    /// <summary>On-demand activation is on when either network type is checked.</summary>
    public static bool IsActive(this OnDemandRules rules) => rules.Ethernet || rules.Wifi;
}
