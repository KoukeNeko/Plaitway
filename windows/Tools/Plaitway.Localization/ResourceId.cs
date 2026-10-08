using System.Text;

namespace Plaitway.Localization;

/// <summary>
/// The resource id of a string, made from its English text so that the id never has to be written down twice:
/// the words in PascalCase, a trailing <c>…</c> as <c>Ellipsis</c>, <c>?</c> as <c>Question</c>, <c>:</c> as
/// <c>Colon</c> and <c>/</c> as <c>Per</c>. Format specifiers add nothing. Two keys that give the same id are an error.
/// </summary>
internal static class ResourceId
{
    private static readonly Dictionary<char, string> PunctuationWords = new()
    {
        ['…'] = "Ellipsis",
        ['?'] = "Question",
        [':'] = "Colon",
        ['/'] = "Per",
    };

    /// <summary>The id of the string with the English text <paramref name="key"/>.</summary>
    public static string From(string key)
    {
        var withoutSpecifiers = FormatSpecifier.LiteralText(key);
        var id = new StringBuilder();
        var startsWord = true;
        foreach (var character in withoutSpecifiers)
        {
            if (char.IsAsciiLetterOrDigit(character))
            {
                id.Append(startsWord ? char.ToUpperInvariant(character) : character);
                startsWord = false;
            }
            else
            {
                startsWord = true;
                if (PunctuationWords.TryGetValue(character, out var word))
                {
                    id.Append(word);
                }
            }
        }

        return id.Length > 0 && !char.IsAsciiDigit(id[0]) ? id.ToString() : throw new CatalogException($"\"{key}\" gives no usable resource id");
    }
}
