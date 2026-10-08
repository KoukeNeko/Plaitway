using System.Globalization;
using Plaitway.AppCore.Text;
using Plaitway.Client.Tests.Support;
using Plaitway.Localization;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>The strings of the catalogs, read from the files the app is built from, in a language the test names.</summary>
internal sealed class CatalogLocalizer : ILocalizer
{
    private readonly Dictionary<string, string> _formats;

    public CatalogLocalizer(StringSet set, string languageTag)
    {
        _formats = set.Strings.ToDictionary(text => text.Id, text => text.Formats[languageTag]);
        Culture = CultureInfo.GetCultureInfo(languageTag);
    }

    public CultureInfo Culture { get; }

    public string Resolve(string id) =>
        _formats.TryGetValue(id, out var format) ? format : throw new KeyNotFoundException($"no string with the id {id}");
}

/// <summary>The catalogs, loaded once, and the text in each language of the app.</summary>
internal static class TestText
{
    public const string English = "en-US";
    public const string TraditionalChinese = "zh-TW";

    public static string MacOsCatalogPath { get; } = Path.Combine(Repository.Root, "macos", "Sources", "PlaitwayMenuBar", "Resources", "Localizable.xcstrings");

    public static string WindowsCatalogPath { get; } = Path.Combine(Repository.Root, "windows", "Resources", "Windows.xcstrings");

    public static StringSet Catalogs { get; } = StringSet.Load([MacOsCatalogPath, WindowsCatalogPath]);

    public static UiText En { get; } = For(English);

    public static UiText Zh { get; } = For(TraditionalChinese);

    public static UiText For(string languageTag) => new(new CatalogLocalizer(Catalogs, languageTag));
}
