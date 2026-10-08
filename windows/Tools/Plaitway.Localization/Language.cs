namespace Plaitway.Localization;

/// <summary>A language of the app and where its text is in a catalog.</summary>
/// <param name="Tag">The Windows language tag, which names the folder of the .resw file.</param>
/// <param name="CatalogTag">The tag in the catalog; null for the language the keys are written in.</param>
internal sealed record Language(string Tag, string? CatalogTag)
{
    /// <summary>English, the keys themselves.</summary>
    public static Language English { get; } = new("en-US", null);

    /// <summary>Traditional Chinese as used in Taiwan.</summary>
    public static Language TraditionalChinese { get; } = new("zh-TW", "zh-Hant");

    /// <summary>The languages the app ships, English first: it is the fallback.</summary>
    public static IReadOnlyList<Language> All { get; } = [English, TraditionalChinese];
}
