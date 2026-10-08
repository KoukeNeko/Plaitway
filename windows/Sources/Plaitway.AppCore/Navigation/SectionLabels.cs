using Plaitway.AppCore.Text;

namespace Plaitway.AppCore.Navigation;

/// <summary>The words on the tabs: one label per page everywhere it appears.</summary>
public static class SectionLabels
{
    /// <summary>The five tabs of a profile, in order.</summary>
    public static IReadOnlyList<ProfileSection> ProfileSections { get; } = Enum.GetValues<ProfileSection>();

    /// <summary>The pages of Diagnostics, in order.</summary>
    public static IReadOnlyList<DiagnosticsPage> DiagnosticsPages { get; } = Enum.GetValues<DiagnosticsPage>();

    /// <summary>The label of a tab of a profile.</summary>
    public static string Label(this ProfileSection section, UiText text) => section switch
    {
        ProfileSection.Overview => text.Overview,
        ProfileSection.Routes => text.RoutesAndDNS,
        ProfileSection.Logs => text.Logs,
        ProfileSection.Configuration => text.Configuration,
        _ => text.Settings,
    };

    /// <summary>The label of a page of Diagnostics.</summary>
    public static string Label(this DiagnosticsPage page, UiText text) => page switch
    {
        DiagnosticsPage.Overview => text.Overview,
        _ => text.HelperLog,
    };
}
