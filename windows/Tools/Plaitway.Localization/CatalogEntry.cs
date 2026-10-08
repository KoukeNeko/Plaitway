namespace Plaitway.Localization;

/// <summary>One string of a catalog: its key, which is the English text, and its translations.</summary>
/// <param name="Key">The English text, with format specifiers such as <c>%@</c> and <c>%lld</c>.</param>
/// <param name="Translations">The translated text by catalog language tag such as <c>zh-Hant</c>.</param>
/// <param name="Source">The catalog file the entry comes from, for messages.</param>
internal sealed record CatalogEntry(string Key, IReadOnlyDictionary<string, Translation> Translations, string Source);

/// <summary>A translation and whether the catalog marks it as done.</summary>
/// <param name="Value">The text.</param>
/// <param name="State">The catalog's state, <c>translated</c> when it is done.</param>
internal sealed record Translation(string Value, string State);
