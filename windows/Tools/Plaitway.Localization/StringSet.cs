namespace Plaitway.Localization;

/// <summary>One string of the app, in every language, with the id the code and the resources know it by.</summary>
/// <param name="Id">The resource id.</param>
/// <param name="Key">The English text with the catalog's format specifiers.</param>
/// <param name="Specifiers">The arguments the string takes.</param>
/// <param name="Formats">The text in each language by Windows language tag, as a .NET composite format.</param>
/// <param name="Source">The catalog the string comes from.</param>
internal sealed record LocalizedString(
    string Id, string Key, IReadOnlyList<FormatSpecifier> Specifiers, IReadOnlyDictionary<string, string> Formats, string Source);

/// <summary>
/// The strings of all catalogs together, checked. The macOS catalog is the source of every string both apps
/// share and the Windows catalog holds the ones that exist only here; a key in both is a mistake, because there
/// would be two sources for one string.
/// </summary>
internal sealed class StringSet
{
    private const string TranslatedState = "translated";
    private const string EnglishCatalogTag = "en";

    private StringSet(IReadOnlyList<LocalizedString> strings, IReadOnlyList<string> problems)
    {
        Strings = strings;
        Problems = problems;
    }

    /// <summary>Every string, in the order of the catalogs.</summary>
    public IReadOnlyList<LocalizedString> Strings { get; }

    /// <summary>What is wrong with the catalogs, one line each; empty when they can be used.</summary>
    public IReadOnlyList<string> Problems { get; }

    /// <summary>Reads and checks the catalogs at <paramref name="paths"/>.</summary>
    /// <exception cref="CatalogException">A catalog cannot be read.</exception>
    public static StringSet Load(IEnumerable<string> paths)
    {
        var problems = new List<string>();
        var strings = new List<LocalizedString>();
        var idOwners = new Dictionary<string, LocalizedString>(StringComparer.Ordinal);
        var keyOwners = new Dictionary<string, string>(StringComparer.Ordinal);
        foreach (var path in paths)
        {
            foreach (var entry in CatalogReader.Read(path))
            {
                AddEntry(entry, strings, idOwners, keyOwners, problems);
            }
        }

        return new StringSet(strings, problems);
    }

    private static void AddEntry(
        CatalogEntry entry,
        List<LocalizedString> strings,
        Dictionary<string, LocalizedString> idOwners,
        Dictionary<string, string> keyOwners,
        List<string> problems)
    {
        if (!keyOwners.TryAdd(entry.Key, entry.Source))
        {
            problems.Add($"\"{entry.Key}\" is in {keyOwners[entry.Key]} and in {entry.Source}");
            return;
        }

        var problemsBefore = problems.Count;
        var formats = FormatsOf(entry, problems);
        var id = ResourceId.From(entry.Key);
        if (idOwners.TryGetValue(id, out var other))
        {
            problems.Add($"\"{entry.Key}\" and \"{other.Key}\" both give the resource id {id}");
            return;
        }

        if (problems.Count > problemsBefore)
        {
            return;
        }

        var added = new LocalizedString(id, entry.Key, FormatSpecifier.In(entry.Key), formats, entry.Source);
        idOwners[id] = added;
        strings.Add(added);
    }

    private static Dictionary<string, string> FormatsOf(CatalogEntry entry, List<string> problems)
    {
        var formats = new Dictionary<string, string>(StringComparer.Ordinal);
        var english = entry.Translations.TryGetValue(EnglishCatalogTag, out var spelledOut) ? spelledOut.Value : entry.Key;
        var expected = FormatSpecifier.In(english);
        foreach (var language in Language.All)
        {
            var text = language.CatalogTag is null ? english : TranslationOf(entry, language, problems);
            if (text is null)
            {
                continue;
            }

            if (!FormatSpecifier.In(text).SequenceEqual(expected))
            {
                problems.Add($"\"{entry.Key}\" -> \"{text}\" ({language.Tag}): the format specifiers differ from the English text");
            }

            formats[language.Tag] = FormatSpecifier.ToCompositeFormat(text);
        }

        return formats;
    }

    private static string? TranslationOf(CatalogEntry entry, Language language, List<string> problems)
    {
        if (!entry.Translations.TryGetValue(language.CatalogTag!, out var translation))
        {
            problems.Add($"\"{entry.Key}\" has no {language.CatalogTag} text");
            return null;
        }

        if (translation.State != TranslatedState)
        {
            problems.Add($"\"{entry.Key}\" is {translation.State} in {language.CatalogTag}, not {TranslatedState}");
        }

        if (string.IsNullOrWhiteSpace(translation.Value))
        {
            problems.Add($"\"{entry.Key}\" is empty in {language.CatalogTag}");
            return null;
        }

        return translation.Value;
    }
}
