using System.Globalization;
using System.Text;
using Plaitway.V1;

namespace Plaitway.Client.Config;

/// <summary>The text holds the placeholder of one secret more than once, so the secret cannot be put back.</summary>
public sealed class DuplicatedPlaceholderException(long number, int line)
    : Exception($"placeholder {number} appears again on line {line}")
{
    /// <summary>The number of the placeholder.</summary>
    public long Number { get; } = number;

    /// <summary>Where the second one stands, 1-based, in the edited text.</summary>
    public int Line { get; } = line;
}

/// <summary>
/// Keeps the secrets of a profile off the screen while it is edited. The mask finds them in the profile
/// text and shows a numbered placeholder such as <c>‹secret 1›</c> in their place; <see cref="Restore"/>
/// puts each secret back where its placeholder still stands after the user's edits.
/// </summary>
/// <remarks>
/// <para>
/// What is a secret: a WireGuard <c>PrivateKey</c> or <c>PresharedKey</c> value (also in a line that is
/// commented out), the body of an OpenVPN <c>&lt;key&gt;</c>, <c>&lt;tls-auth&gt;</c>,
/// <c>&lt;tls-crypt&gt;</c>, <c>&lt;tls-crypt-v2&gt;</c>, <c>&lt;pkcs12&gt;</c>, <c>&lt;secret&gt;</c>,
/// <c>&lt;auth-user-pass&gt;</c> or <c>&lt;http-proxy-user-pass&gt;</c> block, and a PEM private key or
/// OpenVPN static key anywhere else in the text.
/// </para>
/// <para>
/// What an edit does to a placeholder: left alone, its secret comes back; moved with cut and paste, the
/// secret moves with it; deleted or typed over, the secret is gone; copied, <see cref="Restore"/> fails,
/// because it cannot know where the secret belongs; changed to a number the mask does not know, or
/// damaged, it is ordinary text.
/// </para>
/// </remarks>
public sealed class SecretMask
{
    private const string PlaceholderOpening = "‹secret ";
    private const char PlaceholderClosing = '›';
    private const string PemBegin = "-----BEGIN ";
    private const string PemEnd = "-----END ";
    private const string PemDashes = "-----";
    private const string PrivateKeyLabel = "PRIVATE KEY";
    private const string OpenVpnLabelPrefix = "OPENVPN";
    private const string ConnectionBlock = "connection";

    /// <summary>Blocks whose body is a secret.</summary>
    private static readonly HashSet<string> SecretBlocks =
    [
        "key", "tls-auth", "tls-crypt", "tls-crypt-v2", "pkcs12", "secret", "auth-user-pass", "http-proxy-user-pass",
    ];

    private readonly Dictionary<long, string> _secrets = [];

    /// <summary>The text to show and edit.</summary>
    public string DisplayText { get; }

    /// <summary>Finds the secrets of <paramref name="text"/>, a profile of <paramref name="kind"/>, and hides them.</summary>
    public SecretMask(string text, ProfileKind kind)
    {
        // A placeholder that is in the text already is not ours: skip its number.
        var taken = PlaceholdersIn(text).Select(placeholder => placeholder.Number).ToHashSet();
        var display = new StringBuilder();
        var cursor = 0;
        long number = 0;
        foreach (var range in SecretRanges(text, kind))
        {
            do
            {
                number++;
            }
            while (taken.Contains(number));

            display.Append(text, cursor, range.Start - cursor).Append(Placeholder(number));
            _secrets[number] = range.Slice(text);
            cursor = range.End;
        }

        display.Append(text, cursor, text.Length - cursor);
        DisplayText = display.ToString();
    }

    /// <summary>The text with its secrets put back, and where its lines came from.</summary>
    public sealed class Restoration
    {
        private readonly IReadOnlyList<Expansion> _expansions;

        internal Restoration(string text, IReadOnlyList<Expansion> expansions)
        {
            Text = text;
            _expansions = expansions;
        }

        /// <summary>The edited text with every known placeholder replaced by its secret.</summary>
        public string Text { get; }

        /// <summary>
        /// The line of the displayed text that shows what stands on <paramref name="restoredLine"/> (1-based)
        /// of the restored text. The daemon names lines of the text it was sent, and a secret of several
        /// lines shows as one.
        /// </summary>
        public int DisplayLine(int restoredLine)
        {
            var added = 0;
            foreach (var expansion in _expansions)
            {
                var first = expansion.Line + added;
                if (restoredLine < first)
                {
                    break;
                }

                if (restoredLine <= first + expansion.ExtraLines)
                {
                    return expansion.Line;
                }

                added += expansion.ExtraLines;
            }

            return restoredLine - added;
        }

        /// <summary>A placeholder replaced by a secret.</summary>
        /// <param name="Line">Line of the displayed text that holds the placeholder.</param>
        /// <param name="ExtraLines">Lines the secret adds when it replaces the placeholder.</param>
        internal readonly record struct Expansion(int Line, int ExtraLines);
    }

    /// <summary>
    /// Replaces each placeholder of <paramref name="edited"/> by its secret.
    /// </summary>
    /// <exception cref="DuplicatedPlaceholderException">A secret would have to be put in two places.</exception>
    public Restoration Restore(string edited)
    {
        var text = new StringBuilder();
        var expansions = new List<Restoration.Expansion>();
        var restored = new HashSet<long>();
        var cursor = 0;
        var line = 1;
        var scanned = 0;
        foreach (var (range, number) in PlaceholdersIn(edited))
        {
            if (!_secrets.TryGetValue(number, out var secret))
            {
                continue;
            }

            line += CountLineFeeds(edited, scanned, range.Start);
            scanned = range.Start;
            if (!restored.Add(number))
            {
                throw new DuplicatedPlaceholderException(number, line);
            }

            text.Append(edited, cursor, range.Start - cursor).Append(secret);
            cursor = range.End;
            expansions.Add(new Restoration.Expansion(line, secret.Count(character => character == '\n')));
        }

        text.Append(edited, cursor, edited.Length - cursor);
        return new Restoration(text.ToString(), expansions);
    }

    // Placeholders

    private static string Placeholder(long number) =>
        string.Create(CultureInfo.InvariantCulture, $"{PlaceholderOpening}{number}{PlaceholderClosing}");

    /// <summary>
    /// Where <paramref name="text"/> has something that looks like a placeholder, for the view to style.
    /// Whether the mask knows its number is another matter.
    /// </summary>
    public static IReadOnlyList<TextRange> PlaceholderRanges(string text) =>
        [.. PlaceholdersIn(text).Select(placeholder => placeholder.Range)];

    private static List<(TextRange Range, long Number)> PlaceholdersIn(string text)
    {
        var found = new List<(TextRange, long)>();
        var cursor = 0;
        while (text.IndexOf(PlaceholderOpening, cursor, StringComparison.Ordinal) is var start and >= 0)
        {
            cursor = start + PlaceholderOpening.Length;
            var digitsEnd = cursor;
            while (digitsEnd < text.Length && text[digitsEnd] is >= '0' and <= '9')
            {
                digitsEnd++;
            }

            var hasDigits = digitsEnd > cursor && text[cursor] != '0';
            var isClosed = digitsEnd < text.Length && text[digitsEnd] == PlaceholderClosing;
            if (hasDigits && isClosed
                && long.TryParse(text.AsSpan(cursor, digitsEnd - cursor), NumberStyles.None, CultureInfo.InvariantCulture, out var number))
            {
                cursor = digitsEnd + 1;
                found.Add((new TextRange(start, cursor), number));
            }
        }

        return found;
    }

    private static int CountLineFeeds(string text, int start, int end)
    {
        var count = 0;
        for (var index = start; index < end; index++)
        {
            if (text[index] == '\n')
            {
                count++;
            }
        }

        return count;
    }

    // Finding secrets

    /// <summary>
    /// The text to hide, in order of appearance and without overlap. A block that is never closed runs to
    /// the end of the text, as it does for OpenVPN.
    /// </summary>
    private static List<TextRange> SecretRanges(string text, ProfileKind kind)
    {
        var lines = ConfigText.LineRanges(text);
        var readsWireGuard = kind != ProfileKind.Openvpn;
        var readsOpenVpn = kind != ProfileKind.Wireguard;
        var found = new List<TextRange>();
        // The block whose lines are verbatim text and not directives.
        string? verbatimBlock = null;
        // Labels of PEM blocks that have no END line after the last place that was looked: a text of many
        // BEGIN lines and no END would otherwise be searched to its end for each.
        var withoutFooter = new HashSet<string>();
        var index = 0;
        while (index < lines.Count)
        {
            var line = lines[index];
            var next = index + 1;
            string? openedTag;
            TextRange? wireGuardValue;
            if (verbatimBlock is not null && OpenVpnLine.Closes(verbatimBlock, text, line))
            {
                verbatimBlock = null;
            }
            else if (verbatimBlock is null && readsOpenVpn && (openedTag = new OpenVpnLine(text, line).OpenedBlock()) is not null)
            {
                next = ScanOpenedBlock(text, lines, index, openedTag, found, ref verbatimBlock);
            }
            else if (verbatimBlock is null && readsWireGuard && (wireGuardValue = WireGuardSecret(text, line)) is not null)
            {
                found.Add(wireGuardValue.Value);
            }
            else if (PrivateKeyPem(text, lines, index, withoutFooter) is { } pem)
            {
                found.Add(pem.Range);
                next = pem.Last + 1;
            }

            index = next;
        }

        return found;
    }

    /// <summary>Handles the line that opens <paramref name="tag"/>; returns the index of the next line to read.</summary>
    private static int ScanOpenedBlock(
        string text, IReadOnlyList<TextRange> lines, int openingIndex, string tag, List<TextRange> found, ref string? verbatimBlock)
    {
        var bodyStart = openingIndex + 1;
        if (SecretBlocks.Contains(tag))
        {
            var closing = FirstClosingLine(text, lines, bodyStart, tag);
            if (closing > bodyStart)
            {
                var body = new TextRange(lines[bodyStart].Start, lines[closing - 1].End);
                if (HasContent(text, body))
                {
                    found.Add(body);
                }
            }

            return closing + 1;
        }

        if (tag != ConnectionBlock)
        {
            verbatimBlock = tag;
        }

        return bodyStart;
    }

    /// <summary>The index of the first line from <paramref name="from"/> that closes <paramref name="tag"/>, or the number of lines.</summary>
    private static int FirstClosingLine(string text, IReadOnlyList<TextRange> lines, int from, string tag)
    {
        for (var index = from; index < lines.Count; index++)
        {
            if (OpenVpnLine.Closes(tag, text, lines[index]))
            {
                return index;
            }
        }

        return lines.Count;
    }

    private static bool HasContent(string text, TextRange body)
    {
        for (var index = body.Start; index < body.End; index++)
        {
            if (!ConfigText.IsBlank(text[index]) && text[index] != '\n')
            {
                return true;
            }
        }

        return false;
    }

    /// <summary>
    /// The value of a <c>PrivateKey</c> or <c>PresharedKey</c> line. A commented-out key is as secret as a
    /// live one, whether it was commented out with <c>#</c> (wg-quick) or with <c>;</c>, which the command
    /// line client's mask also takes for a comment although the macOS mask does not. The daemon trims every part of the line with Go's <c>strings.TrimSpace</c>, which takes in
    /// the no-break space, the ideographic space and the other Unicode white space, so this reads Unicode
    /// white space and not only the blanks of <see cref="ConfigText.IsBlank"/>: a key behind a no-break
    /// space is a key the daemon accepts.
    /// </summary>
    private static TextRange? WireGuardSecret(string text, TextRange line)
    {
        var cursor = line.Start;
        while (cursor < line.End && (char.IsWhiteSpace(text[cursor]) || text[cursor] is '#' or ';'))
        {
            cursor++;
        }

        var nameEnd = cursor;
        while (nameEnd < line.End && text[nameEnd] != '=' && !char.IsWhiteSpace(text[nameEnd]))
        {
            nameEnd++;
        }

        var name = ConfigText.ToLowerAsDaemon(text[cursor..nameEnd]);
        if (name is not ("privatekey" or "presharedkey"))
        {
            return null;
        }

        var equals = nameEnd;
        while (equals < line.End && char.IsWhiteSpace(text[equals]))
        {
            equals++;
        }

        if (equals >= line.End || text[equals] != '=')
        {
            return null;
        }

        var valueStart = equals + 1;
        var hash = text.IndexOf('#', valueStart, line.End - valueStart);
        var first = valueStart;
        var last = hash < 0 ? line.End : hash;
        while (first < last && char.IsWhiteSpace(text[first]))
        {
            first++;
        }

        while (first < last && char.IsWhiteSpace(text[last - 1]))
        {
            last--;
        }

        return first < last ? new TextRange(first, last) : null;
    }

    /// <summary>
    /// A PEM private key or an OpenVPN static key that starts on line <paramref name="first"/>: from its
    /// BEGIN line to its END line, and the index of the END line. Without an END line it is not one.
    /// </summary>
    private static (TextRange Range, int Last)? PrivateKeyPem(
        string text, IReadOnlyList<TextRange> lines, int first, HashSet<string> withoutFooter)
    {
        var begin = ConfigText.Trimmed(text, lines[first]);
        var header = begin.Slice(text);
        if (!header.StartsWith(PemBegin, StringComparison.Ordinal)
            || !header.EndsWith(PemDashes, StringComparison.Ordinal)
            || header.Length <= PemBegin.Length + PemDashes.Length)
        {
            return null;
        }

        var label = header[PemBegin.Length..^PemDashes.Length];
        var upperLabel = label.ToUpperInvariant();
        var isKey = upperLabel.Contains(PrivateKeyLabel, StringComparison.Ordinal)
            || upperLabel.StartsWith(OpenVpnLabelPrefix, StringComparison.Ordinal);
        // No footer was found after an earlier BEGIN of this label, so there is none after this one.
        if (!isKey || withoutFooter.Contains(label))
        {
            return null;
        }

        var footer = $"{PemEnd}{label}{PemDashes}";
        for (var index = first + 1; index < lines.Count; index++)
        {
            var candidate = ConfigText.Trimmed(text, lines[index]);
            if (candidate.Length == footer.Length && string.CompareOrdinal(text, candidate.Start, footer, 0, footer.Length) == 0)
            {
                return (new TextRange(begin.Start, candidate.End), index);
            }
        }

        withoutFooter.Add(label);
        return null;
    }
}
