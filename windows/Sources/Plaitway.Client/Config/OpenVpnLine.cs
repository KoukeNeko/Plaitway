namespace Plaitway.Client.Config;

/// <summary>
/// One OpenVPN line split into parameters with the rules of OpenVPN's own parser (and the daemon's):
/// blanks separate them, a parameter that starts with a double or single quote runs to the closing
/// quote, a backslash escapes the next character outside single quotes, and a <c>#</c> or <c>;</c>
/// where a parameter would start begins a comment that runs to the end of the line.
/// </summary>
internal sealed class OpenVpnLine
{
    private const char DoubleQuote = '"';
    private const char SingleQuote = '\'';
    private const char Backslash = '\\';

    private readonly string _text;

    /// <summary>Each parameter with its quotes.</summary>
    public IReadOnlyList<TextRange> Fields { get; }

    /// <summary>From the comment character to the end of the line, if the line has a comment.</summary>
    public TextRange? Comment { get; }

    public OpenVpnLine(string text, TextRange line)
    {
        _text = text;
        var fields = new List<TextRange>();
        TextRange? comment = null;
        var cursor = line.Start;
        while (cursor < line.End)
        {
            var character = text[cursor];
            if (ConfigText.IsBlank(character))
            {
                cursor++;
            }
            else if (character is '#' or ';')
            {
                comment = new TextRange(cursor, line.End);
                break;
            }
            else
            {
                var start = cursor;
                cursor = SkipField(text, cursor, line.End);
                fields.Add(new TextRange(start, cursor));
            }
        }

        Fields = fields;
        Comment = comment;
    }

    /// <summary>Where the parameter that starts at <paramref name="start"/> ends.</summary>
    private static int SkipField(string text, int start, int lineEnd)
    {
        var quote = text[start] is DoubleQuote or SingleQuote ? text[start] : (char?)null;
        var cursor = quote is null ? start : start + 1;
        while (cursor < lineEnd)
        {
            var current = text[cursor];
            if (quote is not null)
            {
                if (current == quote)
                {
                    return cursor + 1;
                }
            }
            else if (ConfigText.IsBlank(current))
            {
                break;
            }

            if (current == Backslash && quote != SingleQuote && cursor + 1 < lineEnd)
            {
                cursor++;
            }

            cursor++;
        }

        return cursor;
    }

    /// <summary>
    /// The name of the block that a line opens: a lone <c>&lt;tag&gt;</c> of letters, digits, <c>-</c> and
    /// <c>_</c>, optionally followed by a comment. Lower-case; null when the line opens no block.
    /// </summary>
    public string? OpenedBlock()
    {
        if (Fields.Count != 1)
        {
            return null;
        }

        var field = ConfigText.Unquoted(Fields[0].Slice(_text));
        if (field.Length < 3 || field[0] != '<' || field[^1] != '>')
        {
            return null;
        }

        var tag = field[1..^1];
        return tag.All(ConfigText.IsTagCharacter) ? ConfigText.ToLowerAsDaemon(tag) : null;
    }

    /// <summary>Whether the line closes the block <paramref name="tag"/> (lower-case): <c>&lt;/tag&gt;</c> and nothing else.</summary>
    public static bool Closes(string tag, string text, TextRange line) =>
        ConfigText.EqualsLowercased(ConfigText.Trimmed(text, line).Slice(text), $"</{tag}>");
}
