using System.Globalization;
using System.Text;
using System.Text.RegularExpressions;

namespace Plaitway.Localization;

/// <summary>What a format specifier of the catalog stands for, which decides the parameter type of the generated method.</summary>
internal enum ArgumentKind
{
    Text,
    Number,
}

/// <summary>A <c>%@</c>, <c>%lld</c> or <c>%1$@</c> of a catalog string.</summary>
/// <param name="Index">Zero-based position of the argument.</param>
/// <param name="Kind">What is formatted.</param>
internal readonly record struct FormatSpecifier(int Index, ArgumentKind Kind)
{
    /// <summary>The position-independent shape of the specifiers of <paramref name="text"/>, for comparing two languages.</summary>
    public static IReadOnlyList<FormatSpecifier> In(string text) => [.. Parse(text).Where(token => token.Specifier is not null).Select(token => token.Specifier!.Value).OrderBy(specifier => specifier.Index)];

    /// <summary>
    /// The text as a .NET composite format: <c>%@</c> becomes <c>{0}</c>, <c>%2$@</c> becomes <c>{1}</c>, <c>%%</c> becomes
    /// <c>%</c>, and braces are doubled.
    /// </summary>
    public static string ToCompositeFormat(string text)
    {
        var builder = new StringBuilder();
        foreach (var token in Parse(text))
        {
            builder.Append(token.Specifier is { } specifier ? $"{{{specifier.Index.ToString(CultureInfo.InvariantCulture)}}}" : Escape(token.Literal));
        }

        return builder.ToString();
    }

    /// <summary>The text without its specifiers.</summary>
    public static string LiteralText(string text) => string.Concat(Parse(text).Select(token => token.Literal));

    private static string Escape(string literal) => literal.Replace("{", "{{", StringComparison.Ordinal).Replace("}", "}}", StringComparison.Ordinal);

    private readonly record struct Token(string Literal, FormatSpecifier? Specifier);

    // %[position$](@|lld|ld|d|lu|u|f|s), and %% for a percent sign.
    private static readonly Regex Pattern = new(@"%(?:(?<percent>%)|(?:(?<position>[0-9]+)\$)?(?<type>@|lld|ld|d|llu|lu|u|f|s))", RegexOptions.CultureInvariant);

    private static List<Token> Parse(string text)
    {
        var tokens = new List<Token>();
        var next = 0;
        var cursor = 0;
        foreach (Match match in Pattern.Matches(text))
        {
            if (match.Index > cursor)
            {
                tokens.Add(new Token(text[cursor..match.Index], null));
            }

            cursor = match.Index + match.Length;
            if (match.Groups["percent"].Success)
            {
                tokens.Add(new Token("%", null));
                continue;
            }

            var index = match.Groups["position"].Success
                ? int.Parse(match.Groups["position"].Value, CultureInfo.InvariantCulture) - 1
                : next++;
            tokens.Add(new Token(string.Empty, new FormatSpecifier(index, KindOf(match.Groups["type"].Value))));
        }

        if (cursor < text.Length)
        {
            tokens.Add(new Token(text[cursor..], null));
        }

        return tokens;
    }

    private static ArgumentKind KindOf(string type) => type is "@" or "s" ? ArgumentKind.Text : ArgumentKind.Number;
}
