using Plaitway.V1;

namespace Plaitway.Client.Config;

/// <summary>The part a stretch of profile text plays in it. The role says what the text is; the view decides how it looks.</summary>
public enum ConfigTokenRole
{
    /// <summary>WireGuard: <c>[Interface]</c>, <c>[Peer]</c>.</summary>
    Section,

    /// <summary>A WireGuard key such as <c>Address</c>, an OpenVPN directive such as <c>remote</c>.</summary>
    Directive,

    /// <summary>A value or parameter that has no more specific role.</summary>
    Argument,

    /// <summary>A decimal number.</summary>
    Number,

    /// <summary>An IPv4 or IPv6 address.</summary>
    IPAddress,

    /// <summary>An address with its prefix length, <c>10.0.0.0/8</c>.</summary>
    Cidr,

    /// <summary>A port number.</summary>
    Port,

    /// <summary>A host name.</summary>
    Hostname,

    /// <summary>WireGuard <c>PrivateKey</c>, <c>PublicKey</c> and <c>PresharedKey</c> values.</summary>
    Base64Key,

    /// <summary>From <c>#</c> (or <c>;</c> in OpenVPN) to the end of the line.</summary>
    Comment,

    /// <summary>OpenVPN <c>&lt;ca&gt;</c> and <c>&lt;/ca&gt;</c>.</summary>
    BlockTag,

    /// <summary>The text between the tags of an OpenVPN block, PEM data and all, as one token.</summary>
    BlockBody,
}

/// <summary>A stretch of profile text and the part it plays in it, for syntax highlighting.</summary>
/// <param name="Range">Where the token is in the text that was tokenized. A token never holds a line break, except for a <see cref="ConfigTokenRole.BlockBody"/>.</param>
/// <param name="Role">What the token is.</param>
public readonly record struct ConfigToken(TextRange Range, ConfigTokenRole Role)
{
    /// <summary>The token's text in <paramref name="text"/>, the string that was tokenized.</summary>
    public string TextIn(string text) => Range.Slice(text);
}

/// <summary>
/// Splits profile text into tokens the way the daemon reads it. The text may be wrong or half-written:
/// whatever has no role is left out, and the tokens come in order of position and never overlap.
/// </summary>
public static class ConfigTokenizer
{
    /// <summary>The tokens of <paramref name="text"/>, a profile of <paramref name="kind"/>; none for a kind that is not OpenVPN or WireGuard.</summary>
    public static IReadOnlyList<ConfigToken> Tokens(string text, ProfileKind kind)
    {
        var lexer = new Lexer(text);
        switch (kind)
        {
            case ProfileKind.Openvpn:
                lexer.ScanOpenVpn();
                break;
            case ProfileKind.Wireguard:
                lexer.ScanWireGuard();
                break;
            default:
                break;
        }

        return lexer.Tokens;
    }

    private sealed class Lexer(string text)
    {
        private const string ConnectionBlock = "connection";
        private const char EndpointPortSeparator = ':';

        private readonly List<ConfigToken> _tokens = [];

        public IReadOnlyList<ConfigToken> Tokens => _tokens;

        private void Add(TextRange? range, ConfigTokenRole role)
        {
            if (range is { IsEmpty: false } nonEmpty)
            {
                _tokens.Add(new ConfigToken(nonEmpty, role));
            }
        }

        // WireGuard

        public void ScanWireGuard()
        {
            foreach (var line in ConfigText.LineRanges(text))
            {
                // The daemon cuts a line at the first '#', wherever it stands.
                var hash = text.IndexOf('#', line.Start, line.Length);
                TextRange? comment = hash >= 0 ? new TextRange(hash, line.End) : null;
                var content = ConfigText.Trimmed(text, new TextRange(line.Start, comment?.Start ?? line.End));
                ScanWireGuardContent(content);
                Add(comment, ConfigTokenRole.Comment);
            }
        }

        private void ScanWireGuardContent(TextRange content)
        {
            if (!content.IsEmpty && text[content.Start] == '[')
            {
                Add(content, ConfigTokenRole.Section);
                return;
            }

            var equals = content.IsEmpty ? -1 : text.IndexOf('=', content.Start, content.Length);
            if (equals < 0)
            {
                return;
            }

            var key = ConfigText.Trimmed(text, new TextRange(content.Start, equals));
            Add(key, ConfigTokenRole.Directive);
            ScanWireGuardValue(ConfigText.ToLowerAsDaemon(key.Slice(text)), ConfigText.Trimmed(text, new TextRange(equals + 1, content.End)));
        }

        private void ScanWireGuardValue(string key, TextRange value)
        {
            switch (key)
            {
                case "privatekey" or "publickey" or "presharedkey":
                    Add(value, ConfigTokenRole.Base64Key);
                    break;
                case "address" or "allowedips":
                    AddItems(value, fallback: ConfigTokenRole.Argument);
                    break;
                case "dns":
                    AddItems(value, fallback: ConfigTokenRole.Hostname);
                    break;
                case "endpoint":
                    ScanEndpoint(value);
                    break;
                case "listenport":
                    Add(value, ValueShapes.IsNumber(value.Slice(text)) ? ConfigTokenRole.Port : ConfigTokenRole.Argument);
                    break;
                case "mtu" or "persistentkeepalive" or "fwmark":
                    Add(value, ValueShapes.IsNumber(value.Slice(text)) ? ConfigTokenRole.Number : ConfigTokenRole.Argument);
                    break;
                default:
                    Add(value, ConfigTokenRole.Argument);
                    break;
            }
        }

        private void AddItems(TextRange value, ConfigTokenRole fallback)
        {
            foreach (var item in ItemsOf(value))
            {
                Add(item, ValueShapes.AddressRole(item.Slice(text)) ?? fallback);
            }
        }

        /// <summary><c>host:port</c> or <c>[IPv6]:port</c>.</summary>
        private void ScanEndpoint(TextRange value)
        {
            var colon = value.IsEmpty ? -1 : text.LastIndexOf(EndpointPortSeparator, value.End - 1, value.Length);
            if (colon < 0)
            {
                Add(value, ConfigTokenRole.Argument);
                return;
            }

            var host = new TextRange(value.Start, colon);
            if (host.Length >= 2 && text[host.Start] == '[' && text[host.End - 1] == ']')
            {
                host = new TextRange(host.Start + 1, host.End - 1);
            }

            var port = new TextRange(colon + 1, value.End);
            Add(host, ValueShapes.IsIPAddress(host.Slice(text)) ? ConfigTokenRole.IPAddress : ConfigTokenRole.Hostname);
            Add(port, ValueShapes.IsNumber(port.Slice(text)) ? ConfigTokenRole.Port : ConfigTokenRole.Argument);
        }

        /// <summary>The comma separated items of <paramref name="value"/>, without their blanks.</summary>
        private List<TextRange> ItemsOf(TextRange value)
        {
            var items = new List<TextRange>();
            var start = value.Start;
            while (true)
            {
                var comma = value.IsEmpty ? -1 : text.IndexOf(',', start, value.End - start);
                var end = comma < 0 ? value.End : comma;
                var item = ConfigText.Trimmed(text, new TextRange(start, end));
                if (!item.IsEmpty)
                {
                    items.Add(item);
                }

                if (comma < 0)
                {
                    return items;
                }

                start = comma + 1;
            }
        }

        // OpenVPN

        public void ScanOpenVpn()
        {
            // The block whose lines are verbatim, with the lines seen in it so far.
            (string Tag, TextRange? Body)? block = null;
            foreach (var line in ConfigText.LineRanges(text))
            {
                if (block is { } open)
                {
                    block = ContinueBlock(open, line);
                    continue;
                }

                var parsed = new OpenVpnLine(text, line);
                var tag = parsed.OpenedBlock();
                if (tag is null)
                {
                    ScanDirective(parsed);
                    continue;
                }

                Add(parsed.Fields[0], ConfigTokenRole.BlockTag);
                Add(parsed.Comment, ConfigTokenRole.Comment);
                // The body of a <connection> block is directives.
                if (tag != ConnectionBlock)
                {
                    block = (tag, null);
                }
            }

            // A block that is never closed runs to the end, as it does for OpenVPN.
            Add(block?.Body, ConfigTokenRole.BlockBody);
        }

        private (string Tag, TextRange? Body)? ContinueBlock((string Tag, TextRange? Body) open, TextRange line)
        {
            if (OpenVpnLine.Closes(open.Tag, text, line))
            {
                Add(open.Body, ConfigTokenRole.BlockBody);
                Add(ConfigText.Trimmed(text, line), ConfigTokenRole.BlockTag);
                return null;
            }

            return (open.Tag, new TextRange(open.Body?.Start ?? line.Start, line.End));
        }

        private void ScanDirective(OpenVpnLine line)
        {
            if (line.Fields.Count > 0)
            {
                var name = line.Fields[0];
                var nameText = name.Slice(text);
                var isClosingTag = line.Fields.Count == 1 && nameText.StartsWith("</", StringComparison.Ordinal) && nameText.EndsWith('>');
                Add(name, isClosingTag ? ConfigTokenRole.BlockTag : ConfigTokenRole.Directive);
                var keyword = ConfigText.ToLowerAsDaemon(nameText.TrimStart('-'));
                for (var index = 1; index < line.Fields.Count; index++)
                {
                    var field = line.Fields[index];
                    Add(field, ArgumentRole(index - 1, keyword, field.Slice(text)));
                }
            }

            Add(line.Comment, ConfigTokenRole.Comment);
        }

        private static ConfigTokenRole ArgumentRole(int index, string directive, string field)
        {
            var value = ConfigText.Unquoted(field);
            return (directive, index) switch
            {
                ("remote" or "http-proxy" or "socks-proxy", 0) =>
                    ValueShapes.IsIPAddress(value) ? ConfigTokenRole.IPAddress : ConfigTokenRole.Hostname,
                ("remote" or "http-proxy" or "socks-proxy", 1) or ("port" or "lport" or "rport", 0) =>
                    ValueShapes.IsNumber(value) ? ConfigTokenRole.Port : ConfigTokenRole.Argument,
                _ => ValueShapes.AddressRole(value)
                    ?? (ValueShapes.IsNumber(value) ? ConfigTokenRole.Number : ConfigTokenRole.Argument),
            };
        }
    }
}
