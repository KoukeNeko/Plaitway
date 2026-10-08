using System.Diagnostics;
using Plaitway.Client.Config;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Config;

/// <summary>The behaviour of macos/Tests/PlaitwayTests/Client/SecretMaskTests.swift, case for case.</summary>
public sealed class SecretMaskTests
{
    private const string PrivateKey = "kPRIVATEkMATERIALkAAAAAAAAAAAAAAAAAAAAAAAA=";
    private const string PresharedKey = "kPRESHAREDkMATERIALkBBBBBBBBBBBBBBBBBBBBBB=";

    private const string WireGuardProfile = $"""
        [Interface]
        PrivateKey = {PrivateKey}
        Address = 10.6.0.2/32
        DNS = 10.6.0.1

        [Peer]
        PublicKey = kPUBLICkKEYkCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
        PresharedKey = {PresharedKey}
        AllowedIPs = 0.0.0.0/0
        Endpoint = 203.0.113.5:51820

        """;

    private const string OvpnText = "client\nremote vpn.example.net 1194\n<key>\nKEY BODY\n</key>\n<tls-crypt>\nCRYPT BODY\n</tls-crypt>\nverb 3\n";

    private static SecretMask WireGuard(string text) => new(text, ProfileKind.Wireguard);

    private static SecretMask OpenVpn(string text) => new(text, ProfileKind.Openvpn);

    private static string Restored(SecretMask mask, string edited) => mask.Restore(edited).Text;

    // WireGuard

    [Fact]
    public void HidesTheKeysOfAWireGuardProfile()
    {
        var mask = WireGuard(WireGuardProfile);

        Assert.Equal(
            WireGuardProfile.Replace(PrivateKey, "‹secret 1›", StringComparison.Ordinal).Replace(PresharedKey, "‹secret 2›", StringComparison.Ordinal),
            mask.DisplayText);
        Assert.Equal(WireGuardProfile, Restored(mask, mask.DisplayText));
    }

    [Theory]
    [InlineData("PrivateKey=§")]
    [InlineData("privatekey = §")]
    [InlineData("PRIVATEKEY\t=\t§")]
    [InlineData("   PrivateKey   =   §   ")]
    [InlineData("PrivateKey = §# no space before the comment")]
    [InlineData("PrivateKey = §   # a comment")]
    public void FindsTheValueWhateverTheSpacingAndTheTrailingComment(string line)
    {
        var text = $"[Interface]\n{line}\nAddress = 10.0.0.2/32\n";
        var mask = WireGuard(text.Replace("§", PrivateKey, StringComparison.Ordinal));

        Assert.DoesNotContain(PrivateKey, mask.DisplayText, StringComparison.Ordinal);
        Assert.Contains("Address = 10.0.0.2/32", mask.DisplayText, StringComparison.Ordinal);
        // Only the value is hidden, not what stands around it.
        Assert.Equal(text.Replace("§", "‹secret 1›", StringComparison.Ordinal), mask.DisplayText);
        Assert.Equal(text.Replace("§", PrivateKey, StringComparison.Ordinal), Restored(mask, mask.DisplayText));
    }

    /// <summary>
    /// The daemon trims a line with Go's strings.TrimSpace, which takes in the no-break space, the
    /// ideographic space and the rest of Unicode white space: a key behind one of them is accepted.
    /// </summary>
    [Theory]
    [InlineData("\u00A0")]
    [InlineData("\u3000")]
    [InlineData("\u2003")]
    [InlineData("\u0085")]
    [InlineData("\u000B")]
    [InlineData("\u000C")]
    [InlineData("\u202F")]
    [InlineData("\u1680")]
    public void AKeyBehindUnicodeWhiteSpaceIsHidden(string space)
    {
        foreach (var name in new[] { "PrivateKey", "PresharedKey" })
        {
            foreach (var text in new[]
            {
                $"[Interface]\n{space}{name} = {PrivateKey}\n",
                $"[Interface]\n{name}{space}={space}{PrivateKey}{space}\n",
                $"[Interface]\n#{space}{name}{space}={PrivateKey}\n",
            })
            {
                var mask = WireGuard(text);
                Assert.DoesNotContain(PrivateKey, mask.DisplayText, StringComparison.Ordinal);
                Assert.Contains("‹secret 1›", mask.DisplayText, StringComparison.Ordinal);
                Assert.Equal(text, Restored(mask, mask.DisplayText));
            }
        }
    }

    [Fact]
    public void ACommentAfterTheKeyStaysVisible() =>
        Assert.Equal("PrivateKey = ‹secret 1› # laptop key\n", WireGuard($"PrivateKey = {PrivateKey} # laptop key\n").DisplayText);

    [Fact]
    public void AKeyThatIsCommentedOutIsStillHidden()
    {
        var text = $"[Interface]\n# PrivateKey = {PrivateKey}\n#PresharedKey={PresharedKey}\n";
        var mask = WireGuard(text);
        Assert.Equal("[Interface]\n# PrivateKey = ‹secret 1›\n#PresharedKey=‹secret 2›\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void AKeyWithoutAValueHasNothingToHide()
    {
        const string text = "[Interface]\nPrivateKey =\nPrivateKey = # nothing\nPrivateKey\n";
        Assert.Equal(text, WireGuard(text).DisplayText);
    }

    [Fact]
    public void OtherKeysAndTheirValuesAreLeftAlone()
    {
        const string text = "[Interface]\nPrivateKeyFile = /etc/key\nMyPrivateKey = x\nAddress = 10.0.0.2/32 # PrivateKey = x\n";
        Assert.Equal(text, WireGuard(text).DisplayText);
    }

    [Fact]
    public void KeepsCrLfLineEndingsOfAWireGuardProfile()
    {
        var text = WireGuardProfile.Replace("\n", "\r\n", StringComparison.Ordinal);
        var mask = WireGuard(text);

        Assert.Contains("PrivateKey = ‹secret 1›\r\n", mask.DisplayText, StringComparison.Ordinal);
        Assert.DoesNotContain(PrivateKey, mask.DisplayText, StringComparison.Ordinal);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void KeepsAByteOrderMarkAndTextInOtherScripts()
    {
        var text = $"\uFEFF# 備註 ‹ 🌐\n[Interface]\nPrivateKey = {PrivateKey} # 筆電\n";
        var mask = WireGuard(text);

        Assert.Equal("\uFEFF# 備註 ‹ 🌐\n[Interface]\nPrivateKey = ‹secret 1› # 筆電\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void AKeyOnTheFirstLineIsHiddenBehindAByteOrderMark()
    {
        var text = $"\uFEFFPrivateKey = {PrivateKey}\n";
        var mask = WireGuard(text);
        Assert.Equal("\uFEFFPrivateKey = ‹secret 1›\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    /// <summary>
    /// The daemon lower-cases the key name with Go's strings.ToLower, whose simple Unicode mapping turns
    /// the Kelvin sign into a k; this is what the C# mask must do too.
    /// </summary>
    [Fact]
    public void AKeyNameIsMatchedTheWayTheDaemonLowerCasesIt()
    {
        var text = $"[Interface]\nPrivate\u212Aey = {PrivateKey}\n";
        Assert.DoesNotContain(PrivateKey, WireGuard(text).DisplayText, StringComparison.Ordinal);
    }

    /// <summary>
    /// The same mapping turns the capital I with dot above (U+0130) into an i, which .NET's invariant
    /// lower-casing does not: the daemon reads this key name as PrivateKey.
    /// </summary>
    [Fact]
    public void AKeyNameWithACapitalIWithDotAboveIsStillAKey()
    {
        var text = $"[Interface]\nPr\u0130vateKey = {PrivateKey}\n";

        var mask = WireGuard(text);

        Assert.Equal("[Interface]\nPr\u0130vateKey = ‹secret 1›\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    // OpenVPN

    [Fact]
    public void HidesTheKeyBlocksOfTheRouterProfile()
    {
        var text = Repository.Text("internal/ovpn/testdata/merlin.ovpn");
        var mask = OpenVpn(text);

        var lines = mask.DisplayText.Split('\n');
        // The certificates are public and stay; the private key and the tls-crypt key do not.
        Assert.True(lines.Contains("<ca>") && lines.Contains("bWVybGluIGNh"));
        Assert.Contains("bWVybGluIGNlcnQ=", lines);
        Assert.DoesNotContain("bWVybGluIGtleQ==", lines);
        Assert.DoesNotContain("ffeeddccbbaa99887766554433221100", lines);
        Assert.Contains("<key>\n‹secret 1›\n</key>", mask.DisplayText, StringComparison.Ordinal);
        Assert.Contains("<tls-crypt>\n‹secret 2›\n</tls-crypt>", mask.DisplayText, StringComparison.Ordinal);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void HidesTheKeyBlocksOfAProfileWithCrLf()
    {
        var text = Repository.Text("internal/ovpn/testdata/windows.ovpn");
        Assert.Contains("\r\n", text);
        var mask = OpenVpn(text);

        Assert.Contains("<key>\r\n‹secret 1›\r\n</key>\r\n", mask.DisplayText, StringComparison.Ordinal);
        Assert.Contains("<tls-auth>\r\n‹secret 2›\r\n</tls-auth>", mask.DisplayText, StringComparison.Ordinal);
        Assert.DoesNotContain("ZmFrZSBrZXk=", mask.DisplayText, StringComparison.Ordinal);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void ASecretOfSeveralLinesComesBackWithItsOwnLineBreaks()
    {
        const string body = "-----BEGIN PRIVATE KEY-----\r\nAAAA\r\nBBBB\r\n-----END PRIVATE KEY-----";
        var text = $"client\r\n<key>\r\n{body}\r\n</key>\r\nverb 3\r\n";
        var mask = OpenVpn(text);

        Assert.Equal("client\r\n<key>\r\n‹secret 1›\r\n</key>\r\nverb 3\r\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Theory]
    [InlineData("asus")]
    [InlineData("connection-blocks")]
    public void LeavesTheOtherBlocksAndTheDirectivesAlone(string name)
    {
        var text = Repository.Text($"internal/ovpn/testdata/{name}.ovpn");
        var mask = OpenVpn(text);
        var hidden = text.Contains("<key>", StringComparison.Ordinal) || text.Contains("<tls-", StringComparison.Ordinal);

        Assert.Equal(hidden, mask.DisplayText != text);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
        Assert.True(mask.DisplayText.Contains("<ca>\n-----BEGIN CERTIFICATE-----", StringComparison.Ordinal) || !text.Contains("<ca>", StringComparison.Ordinal));
    }

    [Theory]
    [InlineData("tls-auth")]
    [InlineData("tls-crypt")]
    [InlineData("tls-crypt-v2")]
    [InlineData("pkcs12")]
    [InlineData("secret")]
    [InlineData("auth-user-pass")]
    [InlineData("http-proxy-user-pass")]
    [InlineData("key")]
    public void HidesEverySecretBlock(string tag)
    {
        var text = $"client\n<{tag}>\nline one\nline two\n</{tag}>\nverb 3\n";
        var mask = OpenVpn(text);

        Assert.Equal($"client\n<{tag}>\n‹secret 1›\n</{tag}>\nverb 3\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Theory]
    [InlineData("ca")]
    [InlineData("cert")]
    [InlineData("dh")]
    [InlineData("crl-verify")]
    [InlineData("extra-certs")]
    [InlineData("peer-fingerprint")]
    [InlineData("connection")]
    public void ShowsTheBlocksThatHoldNothingSecret(string tag)
    {
        var text = $"client\n<{tag}>\nline one\nline two\n</{tag}>\nverb 3\n";
        Assert.Equal(text, OpenVpn(text).DisplayText);
    }

    [Fact]
    public void FindsABlockWhateverTheSpacingAroundItsTags()
    {
        const string text = "client\n  <key>  \t\n  body line  \n\t</key>   \n<TLS-CRYPT> # the static key\nkey data\n</tls-crypt>\n";
        var mask = OpenVpn(text);

        Assert.Equal(
            "client\n  <key>  \t\n‹secret 1›\n\t</key>   \n<TLS-CRYPT> # the static key\n‹secret 2›\n</tls-crypt>\n",
            mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void ATagInACommentOrInsideAnotherBlockOpensNothing()
    {
        const string text = "client\n# <key>\n; <tls-auth>\n<ca>\n<key>\nnot a block, a line of the certificate\n</ca>\nverb 3\n";
        Assert.Equal(text, OpenVpn(text).DisplayText);
    }

    [Fact]
    public void ABlockWithoutABodyHidesNothing()
    {
        const string text = "client\n<key>\n</key>\n<tls-auth>\n \t\n\n</tls-auth>\n";
        Assert.Equal(text, OpenVpn(text).DisplayText);
    }

    [Fact]
    public void ABlockThatIsNeverClosedRunsToTheEnd()
    {
        const string text = "client\n<key>\nSECRET LINE ONE\nSECRET LINE TWO\n";
        var mask = OpenVpn(text);

        Assert.Equal("client\n<key>\n‹secret 1›\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void ABlockWithoutALineBreakAtTheEndStillCloses()
    {
        const string text = "client\n<key>\nSECRET\n</key>";
        var mask = OpenVpn(text);
        Assert.Equal("client\n<key>\n‹secret 1›\n</key>", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    // PEM text outside the blocks

    [Fact]
    public void APrivateKeyOutsideAnyBlockIsHidden()
    {
        const string key = "-----BEGIN PRIVATE KEY-----\nAAAA\nBBBB\n-----END PRIVATE KEY-----";
        var text = $"client\n{key}\nverb 3\n";
        var mask = OpenVpn(text);

        Assert.Equal("client\n‹secret 1›\nverb 3\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Theory]
    [InlineData("ENCRYPTED PRIVATE KEY", true)]
    [InlineData("RSA PRIVATE KEY", true)]
    [InlineData("EC PRIVATE KEY", true)]
    [InlineData("OpenVPN Static key V1", true)]
    [InlineData("OpenVPN tls-crypt-v2 client key", true)]
    [InlineData("CERTIFICATE", false)]
    [InlineData("PUBLIC KEY", false)]
    [InlineData("DH PARAMETERS", false)]
    public void HidesPemTextOutsideBlocksOnlyWhenItIsAKey(string label, bool secret)
    {
        var text = $"client\n  -----BEGIN {label}-----\n  AAAA\n  -----END {label}-----\n";
        Assert.Equal(secret, OpenVpn(text).DisplayText != text);
    }

    [Fact]
    public void APrivateKeyInsideAnotherBlockIsHiddenWithoutHidingTheBlock()
    {
        const string text = "client\n<cert>\n-----BEGIN CERTIFICATE-----\nCCCC\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\nKKKK\n-----END PRIVATE KEY-----\n</cert>\n";
        var mask = OpenVpn(text);

        Assert.Equal(
            "client\n<cert>\n-----BEGIN CERTIFICATE-----\nCCCC\n-----END CERTIFICATE-----\n‹secret 1›\n</cert>\n",
            mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void PemTextWithoutItsEndLineIsNotAKey()
    {
        const string text = "client\n-----BEGIN PRIVATE KEY-----\nAAAA\nverb 3\n";
        Assert.Equal(text, OpenVpn(text).DisplayText);
    }

    [Fact]
    public void APrivateKeyInAWireGuardFileIsHiddenToo()
    {
        const string text = "[Interface]\n-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n";
        var mask = WireGuard(text);
        Assert.Equal("[Interface]\n‹secret 1›\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    // Placeholders that were there before

    [Fact]
    public void ATextThatAlreadyHasAPlaceholderGetsNumbersThatDoNotCollideWithIt()
    {
        var text = $"# was ‹secret 1› and ‹secret 3›\n[Interface]\nPrivateKey = {PrivateKey}\nPresharedKey = {PresharedKey}\n";
        var mask = WireGuard(text);

        Assert.Equal(
            "# was ‹secret 1› and ‹secret 3›\n[Interface]\nPrivateKey = ‹secret 2›\nPresharedKey = ‹secret 4›\n",
            mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void FindsPlaceholdersByTheirExactShape()
    {
        const string text = "a ‹secret 1› b ‹secret 12›‹secret 7› ‹secret 0› ‹secret 01› ‹secret › ‹secret x› ‹secret 5 ‹Secret 6› ‹secret 99999999999999999999›";
        var found = SecretMask.PlaceholderRanges(text).Select(range => range.Slice(text));
        Assert.Equal(["‹secret 1›", "‹secret 12›", "‹secret 7›"], found);
    }

    // Edits

    [Fact]
    public void TextTypedNextToAPlaceholderDoesNotTouchTheSecret()
    {
        var mask = WireGuard(WireGuardProfile);
        var edited = mask.DisplayText
            .Replace("PrivateKey = ‹secret 1›", "PrivateKey = ‹secret 1›abc", StringComparison.Ordinal)
            .Replace("PresharedKey = ‹secret 2›", "PresharedKey = xyz‹secret 2›", StringComparison.Ordinal);

        Assert.Equal(
            WireGuardProfile
                .Replace($"PrivateKey = {PrivateKey}", $"PrivateKey = {PrivateKey}abc", StringComparison.Ordinal)
                .Replace($"PresharedKey = {PresharedKey}", $"PresharedKey = xyz{PresharedKey}", StringComparison.Ordinal),
            Restored(mask, edited));
    }

    [Fact]
    public void APlaceholderThatIsDeletedTakesItsSecretWithIt()
    {
        var mask = OpenVpn(OvpnText);
        var edited = mask.DisplayText.Replace("‹secret 1›\n", string.Empty, StringComparison.Ordinal);

        var result = Restored(mask, edited);
        Assert.Equal("client\nremote vpn.example.net 1194\n<key>\n</key>\n<tls-crypt>\nCRYPT BODY\n</tls-crypt>\nverb 3\n", result);
        Assert.DoesNotContain("KEY BODY", result, StringComparison.Ordinal);
    }

    [Fact]
    public void TextTypedOverAPlaceholderReplacesTheSecret()
    {
        var mask = WireGuard(WireGuardProfile);
        var edited = mask.DisplayText.Replace("‹secret 1›", "NEW-PRIVATE-KEY", StringComparison.Ordinal);

        var result = Restored(mask, edited);
        Assert.Contains("PrivateKey = NEW-PRIVATE-KEY\n", result, StringComparison.Ordinal);
        Assert.DoesNotContain(PrivateKey, result, StringComparison.Ordinal);
        Assert.Contains($"PresharedKey = {PresharedKey}\n", result, StringComparison.Ordinal);
    }

    [Theory]
    [InlineData("‹secret 1")]
    [InlineData("secret 1›")]
    [InlineData("‹secret ›")]
    [InlineData("‹secret1›")]
    [InlineData("‹Secret 1›")]
    [InlineData("‹secret 1 ›")]
    [InlineData("secret 1")]
    [InlineData("‹secret 01›")]
    public void ADamagedPlaceholderIsTextAndItsSecretIsGone(string damaged)
    {
        var mask = WireGuard($"[Interface]\nPrivateKey = {PrivateKey}\n");
        var edited = mask.DisplayText.Replace("‹secret 1›", damaged, StringComparison.Ordinal);

        Assert.Equal($"[Interface]\nPrivateKey = {damaged}\n", Restored(mask, edited));
    }

    [Fact]
    public void APlaceholderOfAnUnknownNumberStaysAsItIs()
    {
        var mask = WireGuard($"[Interface]\nPrivateKey = {PrivateKey}\n");
        var edited = mask.DisplayText + "# ‹secret 2› ‹secret 9›\n";

        Assert.Equal($"[Interface]\nPrivateKey = {PrivateKey}\n# ‹secret 2› ‹secret 9›\n", Restored(mask, edited));
    }

    [Fact]
    public void APlaceholderThatIsCopiedIsAnErrorNotAGuess()
    {
        var mask = OpenVpn(OvpnText);
        var edited = mask.DisplayText + "# ‹secret 2›\n";

        var error = Assert.Throws<DuplicatedPlaceholderException>(() => mask.Restore(edited));
        Assert.Equal(2, error.Number);
        Assert.Equal(10, error.Line);
    }

    [Fact]
    public void AMovedPlaceholderCarriesItsSecretAlong()
    {
        var mask = OpenVpn(OvpnText);
        // Cut the first block's placeholder and paste it into the second block, and the other way round.
        var edited = mask.DisplayText
            .Replace("<key>\n‹secret 1›\n", "<key>\n‹secret 2›\n", StringComparison.Ordinal)
            .Replace("<tls-crypt>\n‹secret 2›\n", "<tls-crypt>\n‹secret 1›\n", StringComparison.Ordinal);

        Assert.Equal(
            OvpnText
                .Replace("KEY BODY", "TMP", StringComparison.Ordinal)
                .Replace("CRYPT BODY", "KEY BODY", StringComparison.Ordinal)
                .Replace("TMP", "CRYPT BODY", StringComparison.Ordinal),
            Restored(mask, edited));
    }

    [Fact]
    public void ASecretComesBackAsItWasEvenWhenItLooksLikeAPlaceholder()
    {
        const string text = "client\n<key>\n‹secret 1›\n‹secret 2›\n</key>\n";
        var mask = OpenVpn(text);

        Assert.Equal("client\n<key>\n‹secret 3›\n</key>\n", mask.DisplayText);
        Assert.Equal(text, Restored(mask, mask.DisplayText));
    }

    [Fact]
    public void TheTextOfADisplayWithoutSecretsIsReturnedAsItIs()
    {
        const string text = "client\nremote vpn.example.net 1194\n";
        var mask = OpenVpn(text);
        Assert.Equal(text, mask.DisplayText);
        Assert.Equal("client\nremote other 443\n\n", Restored(mask, "client\nremote other 443\n\n"));
    }

    // Lines

    [Fact]
    public void MapsALineOfTheRestoredTextBackToTheLineOfTheDisplay()
    {
        const string text = "client\n<key>\nA\nB\nC\n</key>\nverb 3\n<tls-auth>\nD\nE\n</tls-auth>\nremote x 1\n";
        var mask = OpenVpn(text);
        Assert.Equal("client\n<key>\n‹secret 1›\n</key>\nverb 3\n<tls-auth>\n‹secret 2›\n</tls-auth>\nremote x 1\n", mask.DisplayText);
        var restoration = mask.Restore(mask.DisplayText);
        Assert.Equal(text, restoration.Text);

        // Every line that is not part of a secret is found again in the display.
        HashSet<string> secretLines = ["A", "B", "C", "D", "E"];
        var restoredLines = text.Split('\n');
        var displayLines = mask.DisplayText.Split('\n');
        for (var index = 0; index < restoredLines.Length; index++)
        {
            var line = restoredLines[index];
            if (line.Length == 0 || secretLines.Contains(line))
            {
                continue;
            }

            var shown = restoration.DisplayLine(index + 1);
            Assert.True(displayLines[shown - 1] == line, $"restored line {index + 1} is {line}");
        }

        // Lines inside a secret map to the line of its placeholder: the key is restored lines 3 to 5, the tls-auth key 9 and 10.
        foreach (var (line, shown) in new[] { (3, 3), (4, 3), (5, 3), (9, 7), (10, 7) })
        {
            Assert.Equal(shown, restoration.DisplayLine(line));
        }
    }

    [Fact]
    public void LinesBeforeTheFirstSecretAndAfterTheLastAreCountedAsTheyAre()
    {
        var mask = WireGuard($"[Interface]\nPrivateKey = {PrivateKey}\nAddress = 10.0.0.2/32\n");
        var restoration = mask.Restore(mask.DisplayText);
        Assert.Equal([1, 2, 3], new[] { 1, 2, 3 }.Select(restoration.DisplayLine));
    }

    [Fact]
    public void TheLineMapFollowsTheEditedTextNotTheOriginalOne()
    {
        var mask = OpenVpn("client\n<key>\nA\nB\n</key>\nverb 3\n");
        // Two lines added above the placeholder; the secret then spans restored lines 5 and 6.
        var restoration = mask.Restore("client\n# one\n# two\n<key>\n‹secret 1›\n</key>\nverb 3\n");

        Assert.Equal("client\n# one\n# two\n<key>\nA\nB\n</key>\nverb 3\n", restoration.Text);
        Assert.Equal([1, 4, 5, 5, 6, 7], new[] { 1, 4, 5, 6, 7, 8 }.Select(restoration.DisplayLine));
    }

    // The corpus of the Go command line client (cmd/plaitway/edit_test.go, TestMaskSecrets). Go prints
    // [hidden] where this mask prints a numbered placeholder, and keeps the BEGIN and END lines of a PEM
    // key, so the cases are compared by what they hide and what they leave visible.

    public static TheoryData<string, string, string, string> GoCases => new()
    {
        { "private key", "PrivateKey = abc=\n", "abc=", "PrivateKey = " },
        { "no spaces", "privatekey=abc", "abc", "privatekey=" },
        { "preshared key", "[Peer]\nPresharedKey = abc=\n", "abc=", "[Peer]\nPresharedKey = " },
        { "comment on line", "  PresharedKey   =   xyz  # note\r\n", "xyz", "  PresharedKey   =   " },
        { "commented out", "# PrivateKey = old\n; privatekey = older\n", "old|older", "# PrivateKey = " },
        { "public values", "PublicKey = abc=\nEndpoint = h:1\nAddress = 10.0.0.2/32\n", string.Empty, "PublicKey = abc=\nEndpoint = h:1\nAddress = 10.0.0.2/32\n" },
        { "openvpn key", "<key>\nSECRET\n</key>\n", "SECRET", "<key>\n" },
        { "certificates", "<ca>\nPUBLIC\n</ca>\n<cert>\nPUBLIC==\n</cert>\n", string.Empty, "<ca>\nPUBLIC\n</ca>\n<cert>\nPUBLIC==\n</cert>\n" },
        { "directive named", "key client.key\ntls-crypt file\n", string.Empty, "key client.key\ntls-crypt file\n" },
        { "indented", "  <tls-crypt> # keep\nA\nB\n  </tls-crypt>\nremote h\n", "A\nB", "  <tls-crypt> # keep\n" },
        { "upper case", "<KEY>\nS\n</KEY>\n", "S", "<KEY>\n" },
        { "never closed", "<key>\nS\nremote h\n", "S\nremote h", "<key>\n" },
        { "never closed, no newline", "<key>", string.Empty, "<key>" },
        { "in a connection", "<connection>\nremote h\n<key>\nS\n</key>\n</connection>\n", "S", "<connection>\nremote h\n<key>\n" },
        { "crlf", "<key>\r\nS\r\n</key>\r\nremote h\r\n", "S", "<key>\r\n" },
        {
            "pem key in a certificate block",
            "<cert>\n-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\nSECRET\n-----END PRIVATE KEY-----\n</cert>\n",
            "SECRET",
            "<cert>\n-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n"
        },
        {
            "pem key kinds",
            "-----BEGIN RSA PRIVATE KEY-----\nA\n-----END RSA PRIVATE KEY-----\n-----BEGIN ENCRYPTED PRIVATE KEY-----\nB\n-----END ENCRYPTED PRIVATE KEY-----\n-----BEGIN OpenVPN Static key V1-----\nC\n-----END OpenVPN Static key V1-----\n",
            "A|B|C",
            string.Empty
        },
        {
            "pem tls-crypt-v2 client key outside a block",
            "<cert>\n-----BEGIN OpenVPN tls-crypt-v2 client key-----\nSECRET\n-----END OpenVPN tls-crypt-v2 client key-----\n</cert>\n",
            "SECRET",
            "<cert>\n"
        },
        { "pem public key", "-----BEGIN PUBLIC KEY-----\nPUB\n-----END PUBLIC KEY-----\n", string.Empty, "-----BEGIN PUBLIC KEY-----\nPUB\n-----END PUBLIC KEY-----\n" },
        { "two blocks", "<key>\nA\n</key>\n<tls-auth>\nB\n</tls-auth>\n", "A|B", "<key>\n" },
    };

    [Theory]
    [MemberData(nameof(GoCases))]
    public void AgreesWithTheGoCommandLineClientOnWhatIsASecret(string name, string input, string hiddenValues, string visiblePrefix)
    {
        var mask = new SecretMask(input, ProfileKind.Unspecified);

        if (hiddenValues.Length == 0)
        {
            Assert.True(input == mask.DisplayText, $"{name}: nothing is hidden, got {mask.DisplayText}");
        }
        else
        {
            foreach (var value in hiddenValues.Split('|'))
            {
                Assert.False(mask.DisplayText.Contains(value, StringComparison.Ordinal), $"{name}: {value} is shown in {mask.DisplayText}");
            }

            Assert.Contains("‹secret ", mask.DisplayText, StringComparison.Ordinal);
            Assert.StartsWith(visiblePrefix, mask.DisplayText, StringComparison.Ordinal);
        }

        Assert.Equal(input, Restored(mask, mask.DisplayText));
    }
}
/// <summary>Random profiles and random edits that leave every placeholder whole.</summary>
public sealed class SecretMaskPropertyTests
{
    private static readonly string[] Insertions =
        ["x", " ", "# note", "\n", "\r\n", "Address = 10.0.0.9/32", "備註", "🌐", "9", "<key>", "</key>", "PrivateKey = "];

    private sealed record RandomProfile(string Text, ProfileKind Kind, IReadOnlyDictionary<int, string> Secrets);

    public static TheoryData<int> Seeds => [.. Enumerable.Range(0, 150)];

    [Theory]
    [MemberData(nameof(Seeds))]
    public void EditsAwayFromPlaceholdersKeepEverySecretByteIdentical(int seed)
    {
        var profile = BuildProfile((ulong)seed);
        var mask = new SecretMask(profile.Text, profile.Kind);

        // The mask finds exactly what the profile was built to hold, and hides all of it.
        var numbers = SecretMask.PlaceholderRanges(mask.DisplayText).Select(range => NumberOf(mask.DisplayText, range));
        Assert.Equal(profile.Secrets.Keys.Order(), numbers);
        foreach (var secret in profile.Secrets.Values)
        {
            Assert.DoesNotContain(secret, mask.DisplayText, StringComparison.Ordinal);
        }

        Assert.Equal(profile.Text, mask.Restore(mask.DisplayText).Text);

        var random = new SeededRandom((ulong)seed + 1_000_003);
        var edited = mask.DisplayText;
        for (var edits = random.Between(1, 8); edits > 0; edits--)
        {
            edited = Edit(edited, random);
        }

        // What 'edited' has to become: its placeholders replaced by the secrets.
        var expected = edited;
        foreach (var range in SecretMask.PlaceholderRanges(edited).Reverse())
        {
            expected = expected.Remove(range.Start, range.Length).Insert(range.Start, profile.Secrets[NumberOf(edited, range)]);
        }

        var result = mask.Restore(edited).Text;
        Assert.Equal(expected, result);
        foreach (var secret in profile.Secrets.Values)
        {
            Assert.Contains(secret, result, StringComparison.Ordinal);
        }
    }

    /// <summary>One insertion, deletion or replacement that leaves every placeholder whole.</summary>
    private static string Edit(string text, SeededRandom random)
    {
        var placeholders = SecretMask.PlaceholderRanges(text);
        // The stretches between placeholders, where an edit may happen.
        var gaps = new List<TextRange>();
        var start = 0;
        foreach (var range in placeholders)
        {
            gaps.Add(new TextRange(start, range.Start));
            start = range.End;
        }

        gaps.Add(new TextRange(start, text.Length));

        var gap = random.Pick(gaps);
        var from = SnapToBoundary(text, gap.Start + random.Between(0, gap.Length));
        var to = SnapToBoundary(text, Math.Min(gap.End, from + random.Between(0, 6)));
        return text[..from] + random.Pick<string>(Insertions) + text[Math.Max(from, to)..];
    }

    /// <summary>An edit never splits a surrogate pair, as typing in an editor never does.</summary>
    private static int SnapToBoundary(string text, int index) =>
        index > 0 && index < text.Length && char.IsLowSurrogate(text[index]) ? index - 1 : index;

    private static int NumberOf(string text, TextRange range) =>
        int.Parse(text.AsSpan(range.Start + "‹secret ".Length, range.Length - "‹secret ".Length - 1), provider: System.Globalization.CultureInfo.InvariantCulture);

    private static RandomProfile BuildProfile(ulong seed)
    {
        var random = new SeededRandom(seed);
        var lineBreak = random.Coin() ? "\n" : "\r\n";
        const string alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        string Base64(int count) => new([.. Enumerable.Range(0, count).Select(_ => alphabet[random.Between(0, alphabet.Length - 1)])]);
        string Blanks() => string.Concat(Enumerable.Repeat(random.Pick<string>([" ", "\t", string.Empty]), random.Between(0, 3)));

        var lines = new List<string>();
        var secrets = new Dictionary<int, string>();
        void AddSecret(string secret) => secrets[secrets.Count + 1] = secret;

        ProfileKind kind;
        if (random.Coin())
        {
            kind = ProfileKind.Wireguard;
            // Every profile has at least one secret.
            var key = Base64(43) + "=";
            lines.Add("[Interface]");
            lines.Add($"PrivateKey = {key}");
            AddSecret(key);
            for (var index = 0; index < random.Between(1, 6); index++)
            {
                switch (random.Between(0, 4))
                {
                    case 0:
                        key = Base64(43) + "=";
                        lines.Add($"{Blanks()}PrivateKey{Blanks()}={Blanks()}{key}{Blanks()}");
                        AddSecret(key);
                        break;
                    case 1:
                        key = Base64(43) + "=";
                        lines.Add($"{Blanks()}PresharedKey = {key} # note {index}");
                        AddSecret(key);
                        break;
                    case 2:
                        lines.Add($"# a comment {Base64(8)} ‹ › 備註");
                        break;
                    case 3:
                        lines.Add(string.Empty);
                        break;
                    default:
                        lines.Add($"Address = 10.{index}.0.2/32");
                        break;
                }
            }

            lines.Add("[Peer]");
            lines.Add($"PublicKey = {Base64(43)}=");
        }
        else
        {
            kind = ProfileKind.Openvpn;
            var key = Base64(40);
            lines.AddRange(["client", "<key>", key, "</key>"]);
            AddSecret(key);
            for (var index = 0; index < random.Between(1, 6); index++)
            {
                switch (random.Between(0, 3))
                {
                    case 0:
                        var body = string.Join(lineBreak, Enumerable.Range(0, random.Between(1, 4)).Select(_ => Base64(32)));
                        var tag = random.Pick<string>(["key", "tls-crypt", "tls-auth", "secret"]);
                        lines.Add($"{Blanks()}<{tag}>{Blanks()}");
                        lines.Add(body);
                        lines.Add($"{Blanks()}</{tag}>{Blanks()}");
                        AddSecret(body);
                        break;
                    case 1:
                        lines.AddRange(["<ca>", "-----BEGIN CERTIFICATE-----", Base64(32), "-----END CERTIFICATE-----", "</ca>"]);
                        break;
                    case 2:
                        lines.Add($"; comment {Base64(6)} ‹ ›");
                        break;
                    default:
                        lines.Add($"remote host{index}.example.net 1194");
                        break;
                }
            }
        }

        return new RandomProfile(string.Join(lineBreak, lines) + lineBreak, kind, secrets);
    }
}

/// <summary>A hostile profile must not make the mask quadratic.</summary>
public sealed class SecretMaskCostTests
{
    [Fact]
    public void ManyBeginLinesWithoutAnEndAreScannedOnce()
    {
        var lines = string.Concat(Enumerable.Repeat("-----BEGIN PRIVATE KEY-----\n", 30_000));
        var text = "client\n<ca>\n" + lines + "</ca>\n";
        var clock = Stopwatch.StartNew();
        var mask = new SecretMask(text, ProfileKind.Openvpn);
        clock.Stop();

        Assert.Equal(text, mask.DisplayText);
        Assert.True(clock.Elapsed < TimeSpan.FromSeconds(3), $"took {clock.Elapsed}");
    }

    [Fact]
    public void AKeyAfterManyUnfinishedOnesOfAnotherLabelIsStillHidden()
    {
        var unfinished = string.Concat(Enumerable.Repeat("-----BEGIN RSA PRIVATE KEY-----\n", 50));
        var text = "<ca>\n" + unfinished + "-----BEGIN PRIVATE KEY-----\nSECRETBODY\n-----END PRIVATE KEY-----\n</ca>\n";
        var mask = new SecretMask(text, ProfileKind.Openvpn);
        Assert.DoesNotContain("SECRETBODY", mask.DisplayText, StringComparison.Ordinal);
        Assert.Equal(text, mask.Restore(mask.DisplayText).Text);
    }
}
