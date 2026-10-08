namespace Plaitway.Client.Config;

/// <summary>A stretch of a text, as UTF-16 code unit offsets: <see cref="Start"/> is in, <see cref="End"/> is not.</summary>
public readonly record struct TextRange(int Start, int End)
{
    /// <summary>The number of code units covered.</summary>
    public int Length => End - Start;

    /// <summary>True when nothing is covered.</summary>
    public bool IsEmpty => End <= Start;

    /// <summary>The covered part of <paramref name="text"/>, the string the range was made for.</summary>
    public string Slice(string text) => text[Start..End];
}

/// <summary>
/// What <see cref="SecretMask"/> and <see cref="ConfigTokenizer"/> share: reading profile text as lines,
/// the way the daemon does. Every character that carries meaning in a profile is ASCII, so all of it
/// works on UTF-16 code units and text in any other script passes through untouched.
/// </summary>
internal static class ConfigText
{
    private const char ByteOrderMark = '\uFEFF';
    private const char LineFeed = '\n';
    private const char CarriageReturn = '\r';
    private const char CapitalIWithDot = 'İ';

    /// <summary>
    /// The lines of the text, each without its line break (LF or CR LF). The text after the last line
    /// break is a line only when it is not empty. A byte order mark at the start belongs to no line, as
    /// for the daemon.
    /// </summary>
    public static IReadOnlyList<TextRange> LineRanges(string text)
    {
        var lines = new List<TextRange>();
        var start = text.StartsWith(ByteOrderMark) ? 1 : 0;
        while (start < text.Length)
        {
            var lineFeed = text.IndexOf(LineFeed, start);
            if (lineFeed < 0)
            {
                lines.Add(new TextRange(start, text.Length));
                break;
            }

            var end = lineFeed > start && text[lineFeed - 1] == CarriageReturn ? lineFeed - 1 : lineFeed;
            lines.Add(new TextRange(start, end));
            start = lineFeed + 1;
        }

        return lines;
    }

    /// <summary><paramref name="range"/> without the blanks (space, tab, CR) at both ends.</summary>
    public static TextRange Trimmed(string text, TextRange range)
    {
        var lower = range.Start;
        var upper = range.End;
        while (lower < upper && IsBlank(text[lower]))
        {
            lower++;
        }

        while (upper > lower && IsBlank(text[upper - 1]))
        {
            upper--;
        }

        return new TextRange(lower, upper);
    }

    /// <summary>Space, tab and CR: what the daemon's parsers treat as blank inside a line.</summary>
    public static bool IsBlank(char character) => character is ' ' or '\t' or CarriageReturn;

    /// <summary>True for the bytes the daemon accepts in an OpenVPN block tag.</summary>
    public static bool IsTagCharacter(char character) =>
        character is >= 'a' and <= 'z' or >= 'A' and <= 'Z' or >= '0' and <= '9' or '-' or '_';

    /// <summary>
    /// <paramref name="text"/> in lower case as the daemon makes it (Go's <c>strings.ToLower</c>). That is
    /// Unicode simple case mapping, under which the Kelvin sign U+212A is a <c>k</c> (as for
    /// <see cref="string.ToLowerInvariant"/>) and the capital I with dot above U+0130 is an <c>i</c> (which
    /// <see cref="string.ToLowerInvariant"/> leaves alone). A key or tag spelled with either is one the
    /// daemon accepts, so it has to be read the same way here.
    /// </summary>
    public static string ToLowerAsDaemon(string text) => text.ToLowerInvariant().Replace(CapitalIWithDot, 'i');

    /// <summary>
    /// Whether <paramref name="text"/> in lower case (see <see cref="ToLowerAsDaemon"/>) is
    /// <paramref name="lowercased"/>; comparing "ignoring case" would not see the Kelvin sign as a <c>k</c>,
    /// and a closing tag the daemon accepts would go unseen.
    /// </summary>
    public static bool EqualsLowercased(string text, string lowercased) => ToLowerAsDaemon(text) == lowercased;

    /// <summary>The text between a pair of matching quotes, or the text itself when it is not quoted.</summary>
    public static string Unquoted(string text) =>
        text.Length > 1 && text[0] is '"' or '\'' && text[^1] == text[0] ? text[1..^1] : text;
}
