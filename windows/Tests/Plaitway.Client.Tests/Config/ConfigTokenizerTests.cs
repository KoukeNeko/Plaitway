using Plaitway.Client.Config;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;
using static Plaitway.Client.Config.ConfigTokenRole;

namespace Plaitway.Client.Tests.Config;

/// <summary>A token as the text it covers and its role, which reads better in an expectation than a range.</summary>
public sealed record Piece(string Text, ConfigTokenRole Role);

/// <summary>The behaviour of macos/Tests/PlaitwayTests/Client/ConfigTokenizerTests.swift, case for case.</summary>
public sealed class ConfigTokenizerTests
{
    private const string PrivateKey = "kPRIVATEkMATERIALkAAAAAAAAAAAAAAAAAAAAAAAA=";
    private const string PublicKey = "kPUBLICkKEYkCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=";
    private const string PresharedKey = "kPRESHAREDkMATERIALkBBBBBBBBBBBBBBBBBBBBBB=";

    private static Piece[] Pieces(string text, ProfileKind kind) =>
        [.. ConfigTokenizer.Tokens(text, kind).Select(token => new Piece(token.TextIn(text), token.Role))];

    private static Piece[] WireGuard(string text) => Pieces(text, ProfileKind.Wireguard);

    private static Piece[] OpenVpn(string text) => Pieces(text, ProfileKind.Openvpn);

    private static Piece P(string text, ConfigTokenRole role) => new(text, role);

    /// <summary>What every token list has to satisfy, whatever the text: in order, inside the text, no overlap, no line breaks.</summary>
    private static void AssertWellFormed(IReadOnlyList<ConfigToken> tokens, string text)
    {
        var previousEnd = 0;
        foreach (var token in tokens)
        {
            Assert.False(token.Range.IsEmpty, $"{token} is empty");
            Assert.True(token.Range.Start >= previousEnd, $"{token} overlaps or precedes the token before it");
            Assert.True(token.Range.End <= text.Length, $"{token} is past the end");
            if (token.Role != BlockBody)
            {
                Assert.False(token.TextIn(text).Contains('\n'), $"{token.Role} spans a line break");
            }

            previousEnd = token.Range.End;
        }
    }

    // WireGuard

    [Fact]
    public void TokensAWgQuickProfile()
    {
        var text = $"""
            # Home router
            [Interface]
            PrivateKey = {PrivateKey}
            Address = 10.6.0.2/32, fd00:6::2/128
            ListenPort = 51820
            DNS = 10.6.0.1, 1.1.1.1, home.lan
            MTU = 1380

            [Peer]
            PublicKey = {PublicKey}
            PresharedKey = {PresharedKey}
            AllowedIPs = 0.0.0.0/0, ::/0
            Endpoint = vpn.example.com:51820
            PersistentKeepalive = 25

            """;

        Assert.Equal(
            [
                P("# Home router", Comment),
                P("[Interface]", Section),
                P("PrivateKey", Directive), P(PrivateKey, Base64Key),
                P("Address", Directive), P("10.6.0.2/32", Cidr), P("fd00:6::2/128", Cidr),
                P("ListenPort", Directive), P("51820", Port),
                P("DNS", Directive), P("10.6.0.1", IPAddress), P("1.1.1.1", IPAddress), P("home.lan", Hostname),
                P("MTU", Directive), P("1380", Number),
                P("[Peer]", Section),
                P("PublicKey", Directive), P(PublicKey, Base64Key),
                P("PresharedKey", Directive), P(PresharedKey, Base64Key),
                P("AllowedIPs", Directive), P("0.0.0.0/0", Cidr), P("::/0", Cidr),
                P("Endpoint", Directive), P("vpn.example.com", Hostname), P("51820", Port),
                P("PersistentKeepalive", Directive), P("25", Number),
            ],
            WireGuard(text));
        AssertWellFormed(ConfigTokenizer.Tokens(text, ProfileKind.Wireguard), text);
    }

    public static TheoryData<string, Piece[]> Endpoints => new()
    {
        { "Endpoint = 203.0.113.5:51820", [P("203.0.113.5", IPAddress), P("51820", Port)] },
        { "Endpoint = [2001:db8::1]:51820", [P("2001:db8::1", IPAddress), P("51820", Port)] },
        { "Endpoint = vpn.example.com:443", [P("vpn.example.com", Hostname), P("443", Port)] },
        { "Endpoint = vpn.example.com", [P("vpn.example.com", Argument)] },
        { "Endpoint = vpn.example.com:https", [P("vpn.example.com", Hostname), P("https", Argument)] },
    };

    [Theory]
    [MemberData(nameof(Endpoints))]
    public void SplitsAnEndpointIntoHostAndPort(string line, Piece[] value) =>
        Assert.Equal([P("Endpoint", Directive), .. value], WireGuard(line));

    [Fact]
    public void ListsAreSplitAtTheCommasWhateverTheSpacing()
    {
        Assert.Equal(
            [P("AllowedIPs", Directive), P("0.0.0.0/1", Cidr), P("128.0.0.0/1", Cidr), P("::/0", Cidr)],
            WireGuard("AllowedIPs=0.0.0.0/1,128.0.0.0/1 ,  ::/0"));
        Assert.Equal(
            [P("DNS", Directive), P("1.1.1.1", IPAddress), P("lan.example", Hostname)],
            WireGuard("DNS = 1.1.1.1,, lan.example,"));
        Assert.Equal([P("Address", Directive), P("10.0.0.2", IPAddress)], WireGuard("Address = 10.0.0.2"));
        Assert.Equal(
            [P("AllowedIPs", Directive), P("10.0.0.0/eight", Argument), P("10.0.0.256/8", Argument)],
            WireGuard("AllowedIPs = 10.0.0.0/eight, 10.0.0.256/8"));
    }

    [Fact]
    public void KeysAreMatchedWithoutRegardForCaseAndSpacing()
    {
        Assert.Equal([P("listenport", Directive), P("51820", Port)], WireGuard("\tlistenport\t=\t51820\t"));
        Assert.Equal([P("PERSISTENTKEEPALIVE", Directive), P("off", Argument)], WireGuard("PERSISTENTKEEPALIVE=off"));
        Assert.Equal([P("Table", Directive), P("off", Argument)], WireGuard("Table = off"));
        Assert.Equal([P("FwMark", Directive), P("0x1234", Argument)], WireGuard("FwMark = 0x1234"));
        Assert.Equal(
            [P("PostUp", Directive), P("iptables -A FORWARD -i %i -j ACCEPT", Argument)],
            WireGuard("PostUp = iptables -A FORWARD -i %i -j ACCEPT"));
        Assert.Equal([P("publickey", Directive), P(PublicKey, Base64Key)], WireGuard($"publickey={PublicKey}"));
    }

    /// <summary>The daemon lower-cases key names with Go's strings.ToLower: U+0130 is an i and the Kelvin sign U+212A is a k.</summary>
    [Fact]
    public void AWireGuardKeyNameIsFoldedTheWayTheDaemonDoesIt()
    {
        Assert.Equal(
            [P("Pr\u0130vateKey", Directive), P(PrivateKey, Base64Key)],
            WireGuard($"Pr\u0130vateKey = {PrivateKey}"));
        Assert.Equal(
            [P("Private\u212Aey", Directive), P(PrivateKey, Base64Key)],
            WireGuard($"Private\u212Aey = {PrivateKey}"));
    }

    [Fact]
    public void ACommentRunsFromTheHashToTheEndOfTheLine()
    {
        Assert.Equal(
            [P("Address", Directive), P("10.0.0.2/32", Cidr), P("# the LAN, 10.0.0.0/8 = nothing", Comment)],
            WireGuard("Address = 10.0.0.2/32 # the LAN, 10.0.0.0/8 = nothing"));
        Assert.Equal([P("[Peer]", Section), P("# laptop", Comment)], WireGuard("[Peer]# laptop"));
        Assert.Equal([P($"#PrivateKey = {PrivateKey}", Comment)], WireGuard($"  #PrivateKey = {PrivateKey}"));
        Assert.Equal([P("PrivateKey", Directive), P("# none yet", Comment)], WireGuard("PrivateKey = # none yet"));
    }

    [Fact]
    public void ALineThatIsNotADirectiveHasNoRole()
    {
        Assert.Empty(WireGuard("just some words\n\n   \n"));
        Assert.Equal([P("value", Argument)], WireGuard("=value"));
        Assert.Equal([P("[Interface", Section)], WireGuard("[Interface"));
    }

    [Fact]
    public void LinesEndingInCrLfHaveNoCrInTheirTokens()
    {
        Assert.Equal(
            [
                P("[Interface]", Section),
                P("Address", Directive), P("10.0.0.2/32", Cidr), P("# lan", Comment),
                P("DNS", Directive), P("1.1.1.1", IPAddress),
            ],
            WireGuard("[Interface]\r\nAddress = 10.0.0.2/32 # lan\r\nDNS = 1.1.1.1\r\n"));
    }

    [Fact]
    public void AByteOrderMarkAtTheStartBelongsToNoToken()
    {
        Assert.Equal(
            [P("[Interface]", Section), P("Address", Directive), P("10.0.0.2/32", Cidr)],
            WireGuard("\uFEFF[Interface]\nAddress = 10.0.0.2/32\n"));
        Assert.Equal([P("client", Directive), P("verb", Directive), P("3", Number)], OpenVpn("\uFEFFclient\nverb 3\n"));
    }

    [Fact]
    public void TextInOtherScriptsKeepsItsRanges()
    {
        const string text = "# 備註 🌐\nAddress = 10.0.0.2/32 # 筆電\nEndpoint = 例え.example:51820\n";
        Assert.Equal(
            [
                P("# 備註 🌐", Comment),
                P("Address", Directive), P("10.0.0.2/32", Cidr), P("# 筆電", Comment),
                P("Endpoint", Directive), P("例え.example", Hostname), P("51820", Port),
            ],
            WireGuard(text));
    }

    // OpenVPN

    [Fact]
    public void TokensAProfileLineByLine()
    {
        var text = """
            # Name: Office
            client
            dev tun
            proto tcp-client
            remote vpn.example.net 1194 ; the main server
            remote 203.0.113.7 443 udp
            keepalive 10 30
            route 192.168.1.0 255.255.255.0
            route-ipv6 2001:db8::/32
            dhcp-option DNS 192.168.1.1
            pull-filter ignore "redirect-gateway"
            port 1194
            <ca>
            -----BEGIN CERTIFICATE-----
            AAAA
            -----END CERTIFICATE-----
            </ca>
            verb 3

            """.ReplaceLineEndings("\n");

        Assert.Equal(
            [
                P("# Name: Office", Comment),
                P("client", Directive),
                P("dev", Directive), P("tun", Argument),
                P("proto", Directive), P("tcp-client", Argument),
                P("remote", Directive), P("vpn.example.net", Hostname), P("1194", Port), P("; the main server", Comment),
                P("remote", Directive), P("203.0.113.7", IPAddress), P("443", Port), P("udp", Argument),
                P("keepalive", Directive), P("10", Number), P("30", Number),
                P("route", Directive), P("192.168.1.0", IPAddress), P("255.255.255.0", IPAddress),
                P("route-ipv6", Directive), P("2001:db8::/32", Cidr),
                P("dhcp-option", Directive), P("DNS", Argument), P("192.168.1.1", IPAddress),
                P("pull-filter", Directive), P("ignore", Argument), P("\"redirect-gateway\"", Argument),
                P("port", Directive), P("1194", Port),
                P("<ca>", BlockTag),
                P("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----", BlockBody),
                P("</ca>", BlockTag),
                P("verb", Directive), P("3", Number),
            ],
            OpenVpn(text));
        AssertWellFormed(ConfigTokenizer.Tokens(text, ProfileKind.Openvpn), text);
    }

    [Fact]
    public void ACommentStartsWhereAParameterWould()
    {
        Assert.Equal(
            [P("# one", Comment), P("; two", Comment), P("# three", Comment), P("; four", Comment)],
            OpenVpn("# one\n; two\n  # three\n\t; four"));
        // In the middle of a parameter '#' and ';' are just characters.
        Assert.Equal(
            [P("dhcp-option", Directive), P("DOMAIN", Argument), P("lan#x;y", Argument)],
            OpenVpn("dhcp-option DOMAIN lan#x;y"));
        Assert.Equal([P("verb", Directive), P("3", Number), P("# chatty", Comment)], OpenVpn("verb 3 # chatty"));
        Assert.Equal(
            [P("verb", Directive), P("3", Number), P("#chatty;still the comment", Comment)],
            OpenVpn("verb 3 #chatty;still the comment"));
    }

    [Fact]
    public void QuotedParametersAreOneTokenWithTheirQuotes()
    {
        Assert.Equal(
            [P("remote", Directive), P("\"vpn.example.net\"", Hostname), P("1194", Port)],
            OpenVpn("remote \"vpn.example.net\" 1194"));
        Assert.Equal(
            [
                P("setenv", Directive), P("NAME", Argument),
                P("\"two words # not a comment\"", Argument), P("'single ; quoted'", Argument),
            ],
            OpenVpn("setenv NAME \"two words # not a comment\" 'single ; quoted'"));
        Assert.Equal(
            [P("push", Directive), P("\"a \\\" quote\"", Argument), P("next", Argument)],
            OpenVpn("push \"a \\\" quote\" next"));
        // A quote that is never closed runs to the end of the line.
        Assert.Equal(
            [P("push", Directive), P("\"open", Argument), P("verb", Directive), P("3", Number)],
            OpenVpn("push \"open\nverb 3"));
    }

    [Theory]
    [InlineData("remote")]
    [InlineData("--remote")]
    [InlineData("http-proxy")]
    [InlineData("socks-proxy")]
    public void ReadsTheHostAndThePortOfAServerDirective(string written)
    {
        Assert.Equal(
            [P(written, Directive), P("proxy.example.net", Hostname), P("8080", Port)],
            OpenVpn($"{written} proxy.example.net 8080"));
        Assert.Equal(
            [P(written, Directive), P("2001:db8::10", IPAddress), P("8080", Port)],
            OpenVpn($"{written} 2001:db8::10 8080"));
    }

    [Fact]
    public void ABlockBodyIsOneTokenWhateverItHolds()
    {
        const string text = "<tls-crypt>\n#\n-----BEGIN OpenVPN Static key V1-----\n  ffee  \n\nremote not a directive 1\n-----END OpenVPN Static key V1-----\n</tls-crypt>\n";
        Assert.Equal(
            [
                P("<tls-crypt>", BlockTag),
                P("#\n-----BEGIN OpenVPN Static key V1-----\n  ffee  \n\nremote not a directive 1\n-----END OpenVPN Static key V1-----", BlockBody),
                P("</tls-crypt>", BlockTag),
            ],
            OpenVpn(text));
    }

    [Fact]
    public void TheTagsOfABlockAreFoundWhateverTheSpacingAroundThem()
    {
        const string text = "  <key>  \t\n  body  \n\t</key>   \n<TLS-AUTH> # static key\nkey data\n</tls-auth>";
        Assert.Equal(
            [
                P("<key>", BlockTag),
                P("  body  ", BlockBody),
                P("</key>", BlockTag),
                P("<TLS-AUTH>", BlockTag), P("# static key", Comment),
                P("key data", BlockBody),
                P("</tls-auth>", BlockTag),
            ],
            OpenVpn(text));
    }

    [Fact]
    public void ABlockWithoutABodyHasNoBodyToken() =>
        Assert.Equal(
            [P("<ca>", BlockTag), P("</ca>", BlockTag), P("verb", Directive), P("3", Number)],
            OpenVpn("<ca>\n</ca>\nverb 3\n"));

    [Fact]
    public void ABlockThatIsNeverClosedRunsToTheEnd() =>
        Assert.Equal(
            [P("verb", Directive), P("3", Number), P("<ca>", BlockTag), P("AAAA\nverb 4", BlockBody)],
            OpenVpn("verb 3\n<ca>\nAAAA\nverb 4\n"));

    [Fact]
    public void ATagInACommentOrAnotherBlockOpensNothing()
    {
        Assert.Equal([P("# <ca>", Comment), P("verb", Directive), P("3", Number)], OpenVpn("# <ca>\nverb 3\n"));
        Assert.Equal([P("<ca>", BlockTag), P("<key>", BlockBody), P("</ca>", BlockTag)], OpenVpn("<ca>\n<key>\n</ca>\n"));
        // Two parameters are not a tag.
        Assert.Equal([P("<ca>", Directive), P("extra", Argument)], OpenVpn("<ca> extra\n"));
    }

    [Fact]
    public void TheLinesOfAConnectionBlockAreDirectives()
    {
        var text = Repository.Text("internal/ovpn/testdata/connection-blocks.ovpn");
        var found = OpenVpn(text);

        Assert.Equal(
            ["<connection>", "</connection>", "<connection>", "</connection>", "<connection>", "</connection>", "<ca>", "</ca>"],
            found.Where(piece => piece.Role == BlockTag).Select(piece => piece.Text));
        Assert.Contains(P("primary.example.com", Hostname), found);
        Assert.Contains(P("2001:db8::10", IPAddress), found);
        Assert.Contains(P("8443", Port), found);
        Assert.Equal(
            ["-----BEGIN CERTIFICATE-----\nY29ubmVjdGlvbiBjYQ==\n-----END CERTIFICATE-----"],
            found.Where(piece => piece.Role == BlockBody).Select(piece => piece.Text));
    }

    [Fact]
    public void ACrLfProfileHasNoCrAtTheEndOfAnyToken()
    {
        var text = Repository.Text("internal/ovpn/testdata/windows.ovpn");
        Assert.Contains("\r\n", text);
        var found = OpenVpn(text);

        Assert.All(found, piece => Assert.False(piece.Text.EndsWith('\r') || piece.Text.StartsWith('\r')));
        Assert.Contains(P("remote", Directive), found);
        Assert.Contains(P("vpn.example.org", Hostname), found);
        Assert.Contains(P("443", Port), found);
        Assert.Equal(P("# Windows-style export, CRLF line endings", Comment), found[0]);
        var key = found.First(piece => piece.Role == BlockBody && piece.Text.Contains("PRIVATE KEY", StringComparison.Ordinal));
        Assert.Equal("-----BEGIN PRIVATE KEY-----\r\nZmFrZSBrZXk=\r\n-----END PRIVATE KEY-----", key.Text);
    }

    [Fact]
    public void AStrayClosingTagIsATag() =>
        Assert.Equal([P("</connection>", BlockTag)], OpenVpn("</connection>\n"));

    [Fact]
    public void TokensTheRouterProfiles()
    {
        var asus = OpenVpn(Repository.Text("internal/ovpn/testdata/asus.ovpn"));
        Assert.Equal(P("# Name: ASUS Router", Comment), asus[0]);
        foreach (var piece in new[]
        {
            P("auth-user-pass", Directive), P("vpn.example.net", Hostname), P("1194", Port), P("tcp-client", Argument),
            P("\"redirect-gateway\"", Argument), P("192.168.1.0", IPAddress), P("255.255.255.0", IPAddress), P("AES-128-CBC", Argument),
        })
        {
            Assert.Contains(piece, asus);
        }

        Assert.Equal(["<ca>", "</ca>", "<cert>", "</cert>", "<key>", "</key>"], asus.Where(piece => piece.Role == BlockTag).Select(piece => piece.Text));
        Assert.Equal(3, asus.Count(piece => piece.Role == BlockBody));

        var merlin = OpenVpn(Repository.Text("internal/ovpn/testdata/merlin.ovpn"));
        foreach (var piece in new[]
        {
            P("# Exported from an ASUSWRT-Merlin router", Comment), P("203.0.113.7", IPAddress), P("udp4", Argument),
            P("AES-256-GCM:AES-128-GCM:AES-128-CBC", Argument), P("192.168.1.1", IPAddress), P("def1", Argument),
        })
        {
            Assert.Contains(piece, merlin);
        }

        Assert.Equal(4, merlin.Count(piece => piece.Role == BlockBody));
    }

    // Robustness

    [Fact]
    public void NoTokensForAnUnknownKind()
    {
        Assert.Empty(ConfigTokenizer.Tokens("client\nremote a 1\n", ProfileKind.Unspecified));
        Assert.Empty(ConfigTokenizer.Tokens("[Interface]\n", (ProfileKind)9));
        Assert.Empty(ConfigTokenizer.Tokens(string.Empty, ProfileKind.Openvpn));
        Assert.Empty(ConfigTokenizer.Tokens(string.Empty, ProfileKind.Wireguard));
    }

    [Theory]
    [InlineData("asus")]
    [InlineData("merlin")]
    [InlineData("windows")]
    [InlineData("connection-blocks")]
    public void TheRepositoryProfilesAreWellFormed(string name)
    {
        var text = Repository.Text($"internal/ovpn/testdata/{name}.ovpn");
        AssertWellFormed(ConfigTokenizer.Tokens(text, ProfileKind.Openvpn), text);
        // A profile of the wrong kind is still read without trouble.
        AssertWellFormed(ConfigTokenizer.Tokens(text, ProfileKind.Wireguard), text);
    }

    public static TheoryData<int> Seeds => [.. Enumerable.Range(0, 300)];

    [Theory]
    [MemberData(nameof(Seeds))]
    public void AnyTextGivesWellFormedTokens(int seed)
    {
        var random = new SeededRandom((ulong)seed);
        const string alphabet = "<>[]=#; \"'\\\t\r\n\n/:,.-_abcdefXYZ019é備🌐‹›";
        var characters = new System.Text.StringBuilder();
        var elements = System.Globalization.StringInfo.GetTextElementEnumerator(alphabet);
        var pieces = new List<string>();
        while (elements.MoveNext())
        {
            pieces.Add((string)elements.Current);
        }

        for (var count = random.Between(0, 240); count > 0; count--)
        {
            characters.Append(random.Pick(pieces));
        }

        var text = characters.ToString();
        foreach (var kind in new[] { ProfileKind.Openvpn, ProfileKind.Wireguard })
        {
            AssertWellFormed(ConfigTokenizer.Tokens(text, kind), text);
        }
    }
}
