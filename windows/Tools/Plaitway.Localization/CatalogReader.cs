using System.Text.Json;

namespace Plaitway.Localization;

/// <summary>Reads String Catalogs (<c>.xcstrings</c>), the format of Xcode that the macOS app keeps its strings in.</summary>
internal static class CatalogReader
{
    private const string StringsProperty = "strings";
    private const string LocalizationsProperty = "localizations";
    private const string StringUnitProperty = "stringUnit";
    private const string ValueProperty = "value";
    private const string StateProperty = "state";
    private const string VariationsProperty = "variations";
    private const string SourceLanguageProperty = "sourceLanguage";
    private const string SourceLanguage = "en";
    private const string TranslatedState = "translated";

    /// <summary>The entries of the catalog at <paramref name="path"/>.</summary>
    /// <exception cref="CatalogException">The file is not a catalog this tool can use.</exception>
    public static IReadOnlyList<CatalogEntry> Read(string path)
    {
        using var document = ParseFile(path);
        var root = document.RootElement;
        if (root.TryGetProperty(SourceLanguageProperty, out var language) && language.GetString() != SourceLanguage)
        {
            throw new CatalogException($"{path}: the source language is {language.GetString()}, the tool reads catalogs whose keys are {SourceLanguage}");
        }

        if (!root.TryGetProperty(StringsProperty, out var strings))
        {
            throw new CatalogException($"{path}: no \"{StringsProperty}\" object");
        }

        return [.. strings.EnumerateObject().Select(property => ReadEntry(path, property))];
    }

    private static JsonDocument ParseFile(string path)
    {
        try
        {
            return JsonDocument.Parse(File.ReadAllText(path));
        }
        catch (Exception error) when (error is IOException or JsonException or UnauthorizedAccessException)
        {
            throw new CatalogException($"{path}: {error.Message}");
        }
    }

    private static CatalogEntry ReadEntry(string path, JsonProperty property)
    {
        var translations = new Dictionary<string, Translation>(StringComparer.Ordinal);
        if (property.Value.TryGetProperty(LocalizationsProperty, out var localizations))
        {
            foreach (var localization in localizations.EnumerateObject())
            {
                translations[localization.Name] = ReadTranslation(path, property.Name, localization);
            }
        }

        return new CatalogEntry(property.Name, translations, path);
    }

    private static Translation ReadTranslation(string path, string key, JsonProperty localization)
    {
        if (localization.Value.TryGetProperty(VariationsProperty, out _))
        {
            // The catalogs hold none, and a .resw string has no plural forms to put them in.
            throw new CatalogException($"{path}: \"{key}\" has {VariationsProperty} in {localization.Name}, which the tool does not convert");
        }

        if (!localization.Value.TryGetProperty(StringUnitProperty, out var unit))
        {
            throw new CatalogException($"{path}: \"{key}\" has no {StringUnitProperty} in {localization.Name}");
        }

        var value = unit.TryGetProperty(ValueProperty, out var text) ? text.GetString() ?? string.Empty : string.Empty;
        var state = unit.TryGetProperty(StateProperty, out var stateText) ? stateText.GetString() ?? string.Empty : TranslatedState;
        return new Translation(value, state);
    }
}

/// <summary>A catalog, or the set of catalogs, cannot be turned into resources.</summary>
internal sealed class CatalogException(string message) : Exception(message);
