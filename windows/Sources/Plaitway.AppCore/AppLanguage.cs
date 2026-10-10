using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Text;

namespace Plaitway.AppCore;

/// <summary>A language the app can be shown in.</summary>
/// <param name="Tag">The language tag, such as <c>zh-TW</c>; null for the language of Windows.</param>
/// <param name="Name">What the picker shows: a language in its own words, and "System default" in the language of the app.</param>
public sealed record LanguageChoice(string? Tag, string Name);

/// <summary>
/// The language of the app. The person chooses one, or none to follow Windows. The strings are loaded when the app starts, so
/// the choice is read at the next start and the run in progress keeps the language it began with. A language named on the
/// command line, which developers use, is not a choice and does not change what a restart means.
/// </summary>
public sealed class AppLanguage
{
    /// <summary>The tag of English, which is the name of its folder of resources.</summary>
    public const string English = "en-US";

    /// <summary>The tag of Traditional Chinese as written in Taiwan, which is the name of its folder of resources.</summary>
    public const string TraditionalChinese = "zh-TW";

    private const string EnglishName = "English";
    private const string TraditionalChineseName = "繁體中文";

    private static readonly string[] KnownTags = [English, TraditionalChinese];

    private readonly ILanguagePreference _preference;

    /// <summary>Makes the language of a run, with the choice that the person had made when it started.</summary>
    /// <param name="preference">Where the choice is kept.</param>
    /// <param name="text">The strings, for the name of "System default".</param>
    public AppLanguage(ILanguagePreference preference, UiText text)
    {
        _preference = preference;
        Choices = [new LanguageChoice(null, text.SystemDefault), new LanguageChoice(English, EnglishName), new LanguageChoice(TraditionalChinese, TraditionalChineseName)];
        ChosenAtStart = Known(preference.Language);
        Chosen = ChosenAtStart;
    }

    /// <summary>What the picker offers: Windows' language first.</summary>
    public IReadOnlyList<LanguageChoice> Choices { get; }

    /// <summary>The tag chosen when this run started, which is what its strings were loaded for; null when they follow Windows.</summary>
    public string? ChosenAtStart { get; }

    /// <summary>The tag chosen for the next start; null when it follows Windows.</summary>
    public string? Chosen { get; private set; }

    /// <summary>The choice is not the language of this run, so a restart shows it.</summary>
    public bool IsRestartNeeded => !string.Equals(Chosen, ChosenAtStart, StringComparison.OrdinalIgnoreCase);

    /// <summary>The tag as the app writes it when the app has strings for it, otherwise null. What is kept on disk may have been edited.</summary>
    public static string? Known(string? tag) =>
        KnownTags.FirstOrDefault(known => string.Equals(known, tag, StringComparison.OrdinalIgnoreCase));

    /// <summary>Keeps the choice for the next start; it stays what it was when it cannot be kept.</summary>
    /// <exception cref="InvalidOperationException">The choice cannot be kept.</exception>
    public void Choose(string? tag)
    {
        var known = Known(tag);
        _preference.Save(known);
        Chosen = known;
    }
}
