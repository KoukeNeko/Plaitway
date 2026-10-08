using System.Diagnostics;
using Plaitway.Client.Import;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;

namespace Plaitway.Client.Tests.Import;

/// <summary>
/// The behaviour of macos/Tests/PlaitwayTests/Client/ProfileImporterTests.swift and of the inlining
/// tests of the command line client (cmd/plaitway/importfile_test.go, importfile_windows_test.go).
/// </summary>
public sealed class ProfileImporterTests
{
    private const string Certificate = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----";
    private const string PrivateKey = "-----BEGIN PRIVATE KEY-----\nMIIEtest\n-----END PRIVATE KEY-----";
    private const string StaticKey = "#\n# 2048 bit OpenVPN static key\n#\n-----BEGIN OpenVPN Static key V1-----\n0123\n-----END OpenVPN Static key V1-----";

    private static async Task<string> InlineAsync(string profile, Dictionary<string, string> files)
    {
        using var directory = TempDirectory.With(files);
        return (await ProfileImporter.InlineReferencedFilesAsync(profile, directory.Path)).Text;
    }

    private static async Task<ProfileImportFailure> FailureOfAsync(string profile, string directory)
    {
        var error = await Assert.ThrowsAsync<ProfileImportException>(() => ProfileImporter.InlineReferencedFilesAsync(profile, directory));
        return error.Failure;
    }

    private static string Slashes(string path) => path.Replace('\\', '/');

    /// <summary>A profile is untrusted: the same file named on every line of 1 MiB would be read and copied for each, and the size was only checked when the result was done.</summary>
    [Fact]
    public async Task StopsInliningAsSoonAsTheResultIsTooLarge()
    {
        var large = "-----BEGIN CERTIFICATE-----\n" + new string('A', 200_000) + "\n-----END CERTIFICATE-----\n";
        var profile = "client\n" + string.Concat(Enumerable.Repeat("ca big.pem\n", 100_000));
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["big.pem"] = large });

        var clock = Stopwatch.StartNew();
        var failure = await FailureOfAsync(profile, directory.Path);

        Assert.IsType<ProfileImportFailure.TooLarge>(failure);
        Assert.True(clock.Elapsed < TimeSpan.FromSeconds(5), $"took {clock.Elapsed}");
    }

    [Fact]
    public async Task InlinesCaCertAndKeyFiles()
    {
        var result = await InlineAsync(
            "client\nremote vpn.example.net 1194\nca ca.crt\ncert client.crt\nkey keys/client.key\nverb 3\n",
            new() { ["ca.crt"] = Certificate + "\n", ["client.crt"] = Certificate, ["keys/client.key"] = PrivateKey + "\n" });

        Assert.Equal(
            $"client\nremote vpn.example.net 1194\n<ca>\n{Certificate}\n</ca>\n<cert>\n{Certificate}\n</cert>\n<key>\n{PrivateKey}\n</key>\nverb 3\n",
            result);
    }

    [Fact]
    public async Task InlinesTlsAuthWithItsKeyDirection() =>
        Assert.Equal(
            $"client\n<tls-auth>\n{StaticKey}\n</tls-auth>\nkey-direction 1\n",
            await InlineAsync("client\ntls-auth ta.key 1\n", new() { ["ta.key"] = StaticKey }));

    [Fact]
    public async Task DoesNotRepeatAKeyDirectionTheProfileAlreadyHas()
    {
        var result = await InlineAsync("key-direction 1\ntls-auth ta.key 1\n", new() { ["ta.key"] = StaticKey });
        Assert.Equal(2, result.Split("key-direction").Length);
    }

    [Fact]
    public async Task InlinesTlsCrypt() =>
        Assert.Equal(
            $"<tls-crypt>\n{StaticKey}\n</tls-crypt>\n",
            await InlineAsync("tls-crypt tc.key\n", new() { ["tc.key"] = StaticKey }));

    [Fact]
    public async Task LeavesInlineBlocksAndComplexLinesAlone()
    {
        var profile = $"client\n# ca ignored.crt\n; key ignored.key\ndh none\nca [inline]\n<ca>\nca not-a-directive-inside-a-block.crt\n{Certificate}\n</ca>\nauth-user-pass\nremote-cert-tls server\n";
        Assert.Equal(profile, await InlineAsync(profile, []));
    }

    [Fact]
    public async Task ResolvesRelativePathsAgainstTheProfilesDirectory()
    {
        using var root = TempDirectory.With(new Dictionary<string, string>
        {
            ["profiles/ca.crt"] = Certificate,
            ["profiles/certs/key.pem"] = PrivateKey,
        });

        var result = await ProfileImporter.InlineReferencedFilesAsync(
            "ca ca.crt\nkey \"certs/key.pem\"\nca certs/../ca.crt\n", root.Resolve("profiles"));

        Assert.Equal($"<ca>\n{Certificate}\n</ca>\n<key>\n{PrivateKey}\n</key>\n<ca>\n{Certificate}\n</ca>\n", result.Text);
    }

    // Files outside the profile's directory

    /// <summary><c>profile/</c> next to <c>outside.pem</c>, which a hostile profile would like to read.</summary>
    private static TempDirectory ProfileDirectoryWithASecretBesideIt() => TempDirectory.With(new Dictionary<string, string>
    {
        ["profile/ca.crt"] = Certificate,
        ["outside.pem"] = PrivateKey,
    });

    private static async Task AssertRefusedOutsideAsync(string profile, string directory, string directive, string path)
    {
        var failure = Assert.IsType<ProfileImportFailure.OutsideProfileDirectory>(await FailureOfAsync(profile, directory));
        Assert.Equal(path, failure.Path);
        Assert.Equal(directive, failure.Directive);
    }

    [Theory]
    [InlineData("../outside.pem")]
    [InlineData("certs/../../outside.pem")]
    public async Task RefusesAPathThatLeavesTheProfilesDirectory(string path)
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        await AssertRefusedOutsideAsync($"key {path}\n", root.Resolve("profile"), "key", path);
    }

    [Fact]
    public async Task RefusesAPathThatLeavesTheProfilesDirectoryWrittenWithBackslashes()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        // The profile writes a backslash twice; it is one in the word that openvpn reads.
        await AssertRefusedOutsideAsync(@"key ..\\outside.pem" + "\n", root.Resolve("profile"), "key", @"..\outside.pem");
    }

    /// <summary>
    /// A path with a drive letter is read when it leads into the profile's directory, as the command line
    /// client reads it; the macOS client refuses every absolute path. Both refuse the profile that names
    /// a file beside its directory.
    /// </summary>
    [Fact]
    public async Task RefusesAnAbsolutePathToAFileOutsideTheProfilesDirectory()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        var path = Slashes(root.Resolve("outside.pem"));
        await AssertRefusedOutsideAsync($"ca {path}\n", root.Resolve("profile"), "ca", path);
    }

    [Fact]
    public async Task ReadsAnAbsolutePathThatLeadsIntoTheProfilesDirectory()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        var path = Slashes(root.Resolve("profile/ca.crt"));
        var result = await ProfileImporter.InlineReferencedFilesAsync($"ca {path}\n", root.Resolve("profile"));
        Assert.Equal($"<ca>\n{Certificate}\n</ca>\n", result.Text);
    }

    [Fact]
    public async Task RefusesAHomeDirectoryPath()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        await AssertRefusedOutsideAsync("key ~/.ssh/id_ed25519\n", root.Resolve("profile"), "key", "~/.ssh/id_ed25519");
    }

    [Fact]
    public async Task RefusesASymlinkThatPointsOutsideTheProfilesDirectory()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        root.LinkOrSkip("profile/link.pem", root.Resolve("outside.pem"), isDirectory: false);
        root.LinkOrSkip("profile/linked", root.Path, isDirectory: true);

        await AssertRefusedOutsideAsync("key link.pem\n", root.Resolve("profile"), "key", "link.pem");
        await AssertRefusedOutsideAsync("key linked/outside.pem\n", root.Resolve("profile"), "key", "linked/outside.pem");
    }

    [Fact]
    public async Task AProfileDirectoryReachedThroughASymlinkIsStillTheProfilesDirectory()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        root.LinkOrSkip("alias", root.Resolve("profile"), isDirectory: true);

        var result = await ProfileImporter.InlineReferencedFilesAsync("ca ca.crt\n", root.Resolve("alias"));

        Assert.Equal($"<ca>\n{Certificate}\n</ca>\n", result.Text);
    }

    [Fact]
    public async Task FollowsASymlinkToARegularFile()
    {
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["real/ca.crt"] = Certificate });
        directory.LinkOrSkip("link.crt", directory.Resolve("real/ca.crt"), isDirectory: false);

        var result = await ProfileImporter.InlineReferencedFilesAsync("ca link.crt\n", directory.Path);

        Assert.Equal($"<ca>\n{Certificate}\n</ca>\n", result.Text);
    }

    [Fact]
    public async Task AJunctionBackOutOfTheProfilesDirectoryIsRefused()
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        // A junction needs no privilege, unlike a symbolic link.
        var junction = root.Resolve("profile/junction");
        var start = Process.Start(new ProcessStartInfo("cmd.exe", $"/c mklink /J \"{junction}\" \"{root.Path}\"")
        {
            CreateNoWindow = true,
            RedirectStandardOutput = true,
            UseShellExecute = false,
        })!;
        await start.WaitForExitAsync();
        Assert.True(Directory.Exists(junction), "the junction was not created");

        await AssertRefusedOutsideAsync("key junction/outside.pem\n", root.Resolve("profile"), "key", "junction/outside.pem");
    }

    [Fact]
    public async Task UnderstandsWindowsLineEndings()
    {
        var result = await InlineAsync(
            $"client\r\nca ca.crt\r\n<cert>\r\n{Certificate}\r\n</cert>\r\nverb 3\r\n", new() { ["ca.crt"] = Certificate });

        Assert.DoesNotContain('\r', result);
        Assert.Contains($"<ca>\n{Certificate}\n</ca>", result, StringComparison.Ordinal);
        Assert.Contains("</cert>\nverb 3", result, StringComparison.Ordinal);
    }

    [Fact]
    public async Task NamesTheMissingFile()
    {
        using var directory = new TempDirectory();
        var failure = Assert.IsType<ProfileImportFailure.MissingFile>(await FailureOfAsync("client\nca nowhere.crt\n", directory.Path));
        Assert.Equal("ca", failure.Directive);
        Assert.EndsWith(@"\nowhere.crt", failure.Path, StringComparison.Ordinal);
    }

    [Fact]
    public async Task RefusesAFileThatIsNotKeyMaterial()
    {
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["notes.txt"] = "just some text\n" });
        var failure = Assert.IsType<ProfileImportFailure.NotKeyMaterial>(await FailureOfAsync("key notes.txt\n", directory.Path));
        Assert.Equal("key", failure.Directive);
    }

    [Fact]
    public async Task RefusesABinaryFile()
    {
        using var directory = new TempDirectory();
        directory.Write("cert.der", [0x30, 0x82, 0x00, 0xFF, 0xFE]);
        Assert.IsType<ProfileImportFailure.NotText>(await FailureOfAsync("cert cert.der\n", directory.Path));
    }

    /// <summary>A hostile profile can name a directory or a device next to it; the Unix test uses a FIFO, which never ends.</summary>
    [Theory]
    [InlineData("sub")]
    [InlineData("NUL")]
    public async Task RefusesToReadWhatIsNotARegularFile(string name)
    {
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["sub/placeholder"] = string.Empty });
        var failure = Assert.IsType<ProfileImportFailure.NotText>(await FailureOfAsync($"ca {name}\n", directory.Path));
        Assert.EndsWith(name, failure.Path, StringComparison.OrdinalIgnoreCase);
    }

    // auth-user-pass

    [Fact]
    public async Task ReplacesAnAuthUserPassFileByItsBareFormAndReturnsWhatItHolds()
    {
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["office.ovpn"] = "client\nremote vpn.example.net 1194\nauth-user-pass creds.txt\nverb 3\n",
            ["creds.txt"] = "alice\ns3cret\n",
        });

        var loaded = await ProfileImporter.LoadAsync(directory.Resolve("office.ovpn"));

        Assert.Equal("client\nremote vpn.example.net 1194\nauth-user-pass\nverb 3\n", System.Text.Encoding.UTF8.GetString(loaded.Content));
        Assert.Equal(new Credentials("alice", "s3cret"), loaded.Credentials);
    }

    [Fact]
    public async Task ReadsCredentialsTheWayOpenVpnDoes()
    {
        // CR LF, trailing lines and spaces inside the password are kept or dropped as openvpn does.
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["pw.txt"] = "bob\r\npass word \r\nignored\r\n" });

        var result = await ProfileImporter.InlineReferencedFilesAsync("AUTH-USER-PASS \"pw.txt\"\n", directory.Path);

        Assert.Equal("auth-user-pass\n", result.Text);
        Assert.Equal(new Credentials("bob", "pass word "), result.Credentials);
    }

    [Fact]
    public async Task LeavesABareAuthUserPassAndItsInlineBlockAlone()
    {
        const string bare = "client\nauth-user-pass\n";
        Assert.Equal(bare, await InlineAsync(bare, []));
        const string block = "<auth-user-pass>\nalice\ns3cret\n</auth-user-pass>\n";
        Assert.Equal(block, await InlineAsync(block, []));
        using var directory = new TempDirectory();
        Assert.Null((await ProfileImporter.InlineReferencedFilesAsync(bare, directory.Path)).Credentials);
    }

    [Theory]
    [InlineData("../outside.pem")]
    [InlineData("/etc/hosts")]
    [InlineData("~/creds.txt")]
    public async Task RefusesAnAuthUserPassFileOutsideTheProfilesDirectory(string path)
    {
        using var root = ProfileDirectoryWithASecretBesideIt();
        await AssertRefusedOutsideAsync($"auth-user-pass {path}\n", root.Resolve("profile"), "auth-user-pass", path);
    }

    [Theory]
    [InlineData("alice\n")]
    [InlineData("alice")]
    [InlineData("\ns3cret\n")]
    [InlineData("alice\n\n")]
    [InlineData("")]
    public async Task RefusesAnAuthUserPassFileWithoutBothLines(string content)
    {
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["creds.txt"] = content });
        var failure = Assert.IsType<ProfileImportFailure.NotCredentials>(await FailureOfAsync("auth-user-pass creds.txt\n", directory.Path));
        Assert.EndsWith(@"\creds.txt", failure.Path, StringComparison.Ordinal);
    }

    [Fact]
    public async Task NamesAnAuthUserPassFileThatIsMissing()
    {
        using var directory = new TempDirectory();
        var failure = Assert.IsType<ProfileImportFailure.MissingFile>(await FailureOfAsync("auth-user-pass nowhere.txt\n", directory.Path));
        Assert.Equal("auth-user-pass", failure.Directive);
        Assert.EndsWith(@"\nowhere.txt", failure.Path, StringComparison.Ordinal);
    }

    // load

    [Fact]
    public async Task LoadsAnOvpnFileWithItsReferencedFiles()
    {
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["office.ovpn"] = "client\nremote vpn.example.net 1194\nca ca.crt\n",
            ["ca.crt"] = Certificate,
        });

        var loaded = await ProfileImporter.LoadAsync(directory.Resolve("office.ovpn"));

        Assert.Equal($"client\nremote vpn.example.net 1194\n<ca>\n{Certificate}\n</ca>\n", System.Text.Encoding.UTF8.GetString(loaded.Content));
    }

    [Fact]
    public async Task PassesAWireGuardFileThroughUnchanged()
    {
        // A line that would be a file reference in an OpenVPN profile.
        const string text = "[Interface]\nPrivateKey = AAAA\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = BBBB\nAllowedIPs = 0.0.0.0/0\nEndpoint = 203.0.113.5:51820\n";
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["home.conf"] = text });

        var loaded = await ProfileImporter.LoadAsync(directory.Resolve("home.conf"));

        Assert.Equal(text, System.Text.Encoding.UTF8.GetString(loaded.Content));
    }

    [Fact]
    public async Task LeavesAWireGuardFileWithALookAlikeDirectiveAlone()
    {
        const string conf = "[Interface]\nPrivateKey = k\nAddress = 10.6.0.2/32\n# ca missing.crt\n[Peer]\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n";
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["home.conf"] = conf, ["odd.txt"] = "something = else\n" });

        Assert.Equal(conf, System.Text.Encoding.UTF8.GetString((await ProfileImporter.LoadAsync(directory.Resolve("home.conf"))).Content));
        // A profile without anything that tells its kind is the daemon's to judge.
        Assert.Equal("something = else\n", System.Text.Encoding.UTF8.GetString((await ProfileImporter.LoadAsync(directory.Resolve("odd.txt"))).Content));
    }

    [Fact]
    public async Task RecognisesAnOpenVpnProfileWithoutTheOvpnExtension()
    {
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["router.conf"] = "client\nremote vpn.example.net\nca ca.crt\n",
            ["ca.crt"] = Certificate,
        });

        var loaded = await ProfileImporter.LoadAsync(directory.Resolve("router.conf"));

        Assert.Contains("<ca>", System.Text.Encoding.UTF8.GetString(loaded.Content), StringComparison.Ordinal);
    }

    [Fact]
    public async Task RefusesAProfileThatIsTooLarge()
    {
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["huge.ovpn"] = string.Concat(Enumerable.Repeat("# padding\n", 120_000)) });
        var error = await Assert.ThrowsAsync<ProfileImportException>(() => ProfileImporter.LoadAsync(directory.Resolve("huge.ovpn")));
        Assert.IsType<ProfileImportFailure.TooLarge>(error.Failure);
    }

    [Fact]
    public async Task RefusesAProfileThatInliningMakesTooLarge()
    {
        var certificate = "-----BEGIN CERTIFICATE-----\n" + new string('A', ProfileImporter.MaxReferencedFileSize - 100) + "\n-----END CERTIFICATE-----\n";
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["big.crt"] = certificate,
            ["p.ovpn"] = "client\n" + string.Concat(Enumerable.Repeat("extra-certs big.crt\n", 5)),
        });

        var error = await Assert.ThrowsAsync<ProfileImportException>(() => ProfileImporter.LoadAsync(directory.Resolve("p.ovpn")));

        Assert.IsType<ProfileImportFailure.TooLarge>(error.Failure);
    }

    [Fact]
    public async Task ReportsAMissingProfile()
    {
        const string path = "/nonexistent/profile.ovpn";
        var error = await Assert.ThrowsAsync<ProfileImportException>(() => ProfileImporter.LoadAsync(path));
        Assert.Equal(path, Assert.IsType<ProfileImportFailure.Unreadable>(error.Failure).Path);
    }

    [Theory]
    [InlineData("ca \"my certs/ca file.crt\"")]
    [InlineData("ca 'my certs/ca file.crt'")]
    [InlineData("ca my\\ certs/ca\\ file.crt")]
    [InlineData("  CA   \"my certs/ca file.crt\"  ")]
    public async Task ReadsPathsTheWayOpenVpnDoes(string line)
    {
        var result = await InlineAsync(line + "\n", new() { ["my certs/ca file.crt"] = Certificate });
        Assert.Equal($"<ca>\n{Certificate}\n</ca>\n", result);
    }

    // The corpus of the command line client (cmd/plaitway/importfile_test.go and importfile_windows_test.go)

    private const string GoCa = "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----";
    private const string GoCert = "-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----";
    private const string GoKey = "-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----";
    private const string GoStatic = "-----BEGIN OpenVPN Static key V1-----\nSTATIC\n-----END OpenVPN Static key V1-----";

    private static TempDirectory GoProfileDirectory() => TempDirectory.With(new Dictionary<string, string>
    {
        ["ca.crt"] = GoCa + "\n",
        ["client.crt"] = GoCert + "\r\n", // a file written on Windows
        ["client.key"] = "\n" + GoKey + "\n\n",
        ["ta.key"] = GoStatic + "\n",
        ["my ca.crt"] = GoCa + "\n",
        ["sub/other.crt"] = GoCert + "\n",
        ["readme.txt"] = "not key material\n",
        ["binary.crt"] = "-----BEGIN CERTIFICATE-----\0\n",
    });

    public static TheoryData<string, string, string> GoInlineCases => new()
    {
        { "files become blocks", "ca ca.crt\ncert client.crt\nkey client.key\n", $"<ca>\n{GoCa}\n</ca>\n<cert>\n{GoCert}\n</cert>\n<key>\n{GoKey}\n</key>\n" },
        { "tls-auth keeps its direction", "tls-auth ta.key 1\n", $"<tls-auth>\n{GoStatic}\n</tls-auth>\nkey-direction 1\n" },
        { "the direction is not repeated", "key-direction 0\ntls-auth ta.key 1\n", $"key-direction 0\n<tls-auth>\n{GoStatic}\n</tls-auth>\n" },
        { "tls-auth without a direction", "tls-auth ta.key\n", $"<tls-auth>\n{GoStatic}\n</tls-auth>\n" },
        {
            "the other directives with an inline form",
            "crl-verify ca.crt\nextra-certs ca.crt\ntls-crypt ta.key\ntls-crypt-v2 ta.key\ndh ca.crt\n",
            $"<crl-verify>\n{GoCa}\n</crl-verify>\n<extra-certs>\n{GoCa}\n</extra-certs>\n<tls-crypt>\n{GoStatic}\n</tls-crypt>\n<tls-crypt-v2>\n{GoStatic}\n</tls-crypt-v2>\n<dh>\n{GoCa}\n</dh>\n"
        },
        {
            "subdirectory, quotes, tabs and case",
            "CA\t\"my ca.crt\"\nCert sub/other.crt\nkey './client.key'\n",
            $"<ca>\n{GoCa}\n</ca>\n<cert>\n{GoCert}\n</cert>\n<key>\n{GoKey}\n</key>\n"
        },
        { "line endings become LF", "client\r\nca ca.crt\r\nremote x\r\n", $"client\n<ca>\n{GoCa}\n</ca>\nremote x\n" },
        {
            "what needs no file is left alone",
            "dh none\nca [inline]\n# ca nothing.crt\n; key nothing.key\nremote x 1194\nauth-user-pass\n",
            "dh none\nca [inline]\n# ca nothing.crt\n; key nothing.key\nremote x 1194\nauth-user-pass\n"
        },
        {
            "inline blocks are left alone",
            $"<ca>\n{GoCa}\n</ca>\n<connection>\nremote y\nca missing.crt\n</connection>\n<extra>\nkey missing.key\n</extra>\nca ca.crt\n",
            $"<ca>\n{GoCa}\n</ca>\n<connection>\nremote y\nca missing.crt\n</connection>\n<extra>\nkey missing.key\n</extra>\n<ca>\n{GoCa}\n</ca>\n"
        },
        { "no trailing newline", "ca ca.crt", $"<ca>\n{GoCa}\n</ca>" },
    };

    [Theory]
    [MemberData(nameof(GoInlineCases))]
    public async Task InlinesLikeTheGoCommandLineClient(string name, string input, string expected)
    {
        using var directory = GoProfileDirectory();
        var result = await ProfileImporter.InlineReferencedFilesAsync(input, directory.Path);
        Assert.True(expected == result.Text, $"{name}: got {result.Text}");
    }

    public static TheoryData<string, string> GoRefusals => new()
    {
        { "parent directory", "key ../outside-dir/secret.pem" },
        { "link to a file", "key link.key" },
        { "link to a directory", "key linked-dir/secret.pem" },
        { "home directory", "key ~/.ssh/id_rsa" },
        { "missing", "ca nothing.crt" },
        { "not key material", "ca readme.txt" },
        { "not text", "ca binary.crt" },
        { "directory", "ca sub" },
        { "device", "ca NUL" },
        { "escape in a directory", "ca sub/../../x.crt" },
        { "the root of the drive", @"ca \\certs\\ca.crt" },
        { "the root, forward slashes", "ca /certs/ca.crt" },
        { "a drive and no root", "ca C:ca.crt" },
        { "a drive and a relative dir", "ca C:certs/ca.crt" },
        { "a network share", @"ca \\\\server\\share\\ca.crt" },
        { "a network share, forward slashes", "ca //server/share/ca.crt" },
        { "the extended-length form of a network share", @"ca \\\\?\\UNC\\server\\share\\ca.crt" },
        { "a device path", @"ca \\\\.\\pipe\\plaitway" },
    };

    [Theory]
    [MemberData(nameof(GoRefusals))]
    public async Task RefusesWhatTheGoCommandLineClientRefuses(string name, string line)
    {
        using var directory = GoProfileDirectory();
        using var outside = new TempDirectory();
        outside.Write("secret.pem", GoKey + "\n");
        // A link inside the directory does not make a file outside it inside.
        if (name.StartsWith("link", StringComparison.Ordinal))
        {
            directory.LinkOrSkip("link.key", outside.Resolve("secret.pem"), isDirectory: false);
            directory.LinkOrSkip("linked-dir", outside.Path, isDirectory: true);
        }

        var written = line.Replace("outside-dir", Path.GetFileName(outside.Path), StringComparison.Ordinal);
        directory.Write("certs/ca.crt", GoCa + "\n");

        var error = await Assert.ThrowsAsync<ProfileImportException>(() => ProfileImporter.InlineReferencedFilesAsync(written + "\n", directory.Path));

        Assert.True(error.Failure is not null, name);
    }

    [Fact]
    public async Task ReadsALinkThatStaysInsideTheDirectory()
    {
        using var directory = GoProfileDirectory();
        directory.LinkOrSkip("same-dir.crt", "ca.crt", isDirectory: false);
        var result = await ProfileImporter.InlineReferencedFilesAsync("ca same-dir.crt\n", directory.Path);
        Assert.Contains(GoCa, result.Text, StringComparison.Ordinal);
    }

    [Fact]
    public async Task RefusesAFileThatIsTooLargeToBeKeyMaterial()
    {
        using var directory = GoProfileDirectory();
        directory.Write("big.crt", "-----BEGIN CERTIFICATE-----\n" + new string('A', ProfileImporter.MaxReferencedFileSize));
        Assert.IsType<ProfileImportFailure.TooLarge>(await FailureOfAsync("ca big.crt\n", directory.Path));
    }

    public static TheoryData<string, string> WindowsSpellings => new()
    {
        { "backslashes, written twice", @"ca sub\\..\\ca.crt" },
        { "another case", "ca CA.CRT" },
        { "quoted, with a space", "ca \"my ca.crt\"" },
        { "forward slashes", "ca ./sub/../ca.crt" },
    };

    /// <summary>Windows spells a path in ways that Unix does not, and a file system that ignores case.</summary>
    [Theory]
    [MemberData(nameof(WindowsSpellings))]
    public async Task AcceptsWindowsSpellingsOfAFileInTheProfileDirectory(string name, string line)
    {
        using var directory = GoProfileDirectory();
        var result = await ProfileImporter.InlineReferencedFilesAsync(line + "\n", directory.Path);
        Assert.True($"<ca>\n{GoCa}\n</ca>\n" == result.Text, name);
    }

    [Fact]
    public async Task AcceptsTheDirectoryInAnotherCaseAndInTheExtendedLengthForm()
    {
        using var directory = GoProfileDirectory();
        var expected = $"<ca>\n{GoCa}\n</ca>\n";

        Assert.Equal(expected, (await ProfileImporter.InlineReferencedFilesAsync("ca ca.crt\n", directory.Path.ToUpperInvariant())).Text);
        Assert.Equal(expected, (await ProfileImporter.InlineReferencedFilesAsync("ca ca.crt\n", @"\\?\" + directory.Path)).Text);
    }

    [Fact]
    public async Task AcceptsTheAbsolutePathWithBackslashesWrittenTwice()
    {
        using var directory = GoProfileDirectory();
        var written = directory.Resolve("ca.crt").Replace(@"\", @"\\", StringComparison.Ordinal);
        var result = await ProfileImporter.InlineReferencedFilesAsync($"ca {written}\n", directory.Path);
        Assert.Equal($"<ca>\n{GoCa}\n</ca>\n", result.Text);
    }

    [Fact]
    public async Task ARefusedNetworkPathIsNeverOpened()
    {
        // 192.0.2.0/24 is TEST-NET-1: a connection attempt would hang rather than be refused at once.
        using var directory = new TempDirectory();
        var clock = Stopwatch.StartNew();

        var failure = await FailureOfAsync("ca //192.0.2.1/share/ca.crt\n", directory.Path);

        Assert.IsType<ProfileImportFailure.OutsideProfileDirectory>(failure);
        Assert.True(clock.Elapsed < TimeSpan.FromSeconds(1), $"took {clock.Elapsed}: the path may have been opened");
    }
}
