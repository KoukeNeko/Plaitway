using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

[assembly: AssemblyFixture(typeof(DaemonBinary))]

namespace Plaitway.AppCore.Tests;

/// <summary>macos/Tests/PlaitwayTests/App/AppModelTests.swift, "import", against the real daemon.</summary>
public sealed class AppModelImportTests(DaemonBinary binary)
{
    private const string Certificate = "-----BEGIN CERTIFICATE-----\nMIIBtest\n-----END CERTIFICATE-----\n";

    [Fact]
    public async Task ImportsDroppedFilesInlinesTheirReferencesAndSelectsTheLastProfile()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["office.ovpn"] = "client\nremote vpn.example.net 1194\nca ca.crt\n",
            ["ca.crt"] = Certificate,
            ["home.conf"] = Fixture.WireGuardText(),
        });

        await app.RunAsync(async () =>
        {
            await app.Model.ImportFilesAsync([directory.Resolve("office.ovpn"), directory.Resolve("home.conf")]);

            Assert.Equal(["office", "home"], app.Store.Profiles.Select(profile => profile.Name));
            Assert.Equal([ProfileKind.Openvpn, ProfileKind.Wireguard], app.Store.Profiles.Select(profile => profile.Kind));
            Assert.Null(app.Model.ImportReport);
            Assert.Equal(SidebarItem.Profile(app.Store.Profiles[^1].Id), app.Model.Selection);
        });
    }

    [Fact]
    public async Task ImportsAProfileWhateverItsFileIsCalled()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["work.vpn"] = "client\nremote vpn.example.net 1194\n",
            ["wgs_client-4"] = Fixture.WireGuardText(),
            ["home.WG"] = Fixture.WireGuardText(),
        });

        await app.RunAsync(async () =>
        {
            string[] names = ["work.vpn", "wgs_client-4", "home.WG"];
            await app.Model.ImportFilesAsync(names.Select(directory.Resolve));

            Assert.Equal([ProfileKind.Openvpn, ProfileKind.Wireguard, ProfileKind.Wireguard], app.Store.Profiles.Select(profile => profile.Kind));
            Assert.Null(app.Model.ImportReport);
        });
    }

    [Fact]
    public async Task ReportsWhatWentWrongPerFile()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["fine.ovpn"] = "client\nremote vpn.example.net 1194\n",
            ["scripted.ovpn"] = "client\nremote vpn.example.net 1194\nup /bin/sh\n",
            ["bad.ovpn"] = "client\n# fake: reject\n",
            ["missing.ovpn"] = "client\nca nowhere.crt\n",
            ["notes.txt"] = "client\n# fake: reject\n",
        });

        await app.RunAsync(async () =>
        {
            string[] names = ["fine.ovpn", "scripted.ovpn", "bad.ovpn", "missing.ovpn", "notes.txt", "gone.ovpn"];
            await app.Model.ImportFilesAsync(names.Select(directory.Resolve));

            var report = Assert.IsType<ImportReport>(app.Model.ImportReport);
            Assert.True(report.NeedsAttention);
            Assert.Equal(names, report.Outcomes.Select(outcome => outcome.Filename));

            Assert.Empty(Assert.IsType<ImportOutcome.Imported>(report.Outcomes[0]).Warnings);
            var scripted = Assert.IsType<ImportOutcome.Imported>(report.Outcomes[1]);
            Assert.Equal(["up"], scripted.Warnings.Select(warning => warning.Directive));
            Assert.Contains("rejected", Assert.IsType<ImportOutcome.Failed>(report.Outcomes[2]).Message, StringComparison.Ordinal);
            Assert.Contains("nowhere.crt", Assert.IsType<ImportOutcome.Failed>(report.Outcomes[3]).Message, StringComparison.Ordinal);

            // The extension decides nothing: the daemon judged the content of notes.txt.
            Assert.Contains("rejected", Assert.IsType<ImportOutcome.Failed>(report.Outcomes[4]).Message, StringComparison.Ordinal);
            Assert.IsType<ImportOutcome.Failed>(report.Outcomes[5]);

            // Only the two that the daemon accepted exist.
            Assert.Equal(["fine", "scripted"], app.Store.Profiles.Select(profile => profile.Name));
        });
    }

    [Fact]
    public async Task AFailedImportSelectsNothing()
    {
        await using var app = await AppHarness.StartAsync(binary);
        using var directory = TempDirectory.With(new Dictionary<string, string> { ["bad.ovpn"] = "client\n# fake: reject\n" });

        await app.RunAsync(async () =>
        {
            await app.Model.ImportFilesAsync([directory.Resolve("bad.ovpn")]);

            Assert.Null(app.Model.Selection);
            Assert.Empty(app.Store.Profiles);
        });
    }

    [Fact]
    public async Task AnAuthUserPassFileBecomesSavedCredentialsAndTheFirstConnectionDoesNotAsk()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["router.ovpn"] = "client\nremote vpn.example.net 1194\n# fake: needs-credentials\nauth-user-pass creds.txt\n",
            ["creds.txt"] = "alice\ns3cret\n",
        });

        await app.RunAsync(async () =>
        {
            await app.Model.ImportFilesAsync([directory.Resolve("router.ovpn")]);

            Assert.Null(app.Model.ImportReport);
            var id = app.Store.Profiles[0].Id;
            Assert.Equal(new Credentials("alice", "s3cret"), await saved.GetAsync(id, CredentialKind.UserPassword));

            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the connection", () =>
            {
                Assert.Empty(app.Store.CredentialPrompts);
                return app.Store.Find(id)?.State == ProfileState.Connected;
            });
        });
    }

    [Fact]
    public async Task AProfileThatNamesAFileOutsideItsFolderIsRefusedAndNothingIsStored()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = new InMemoryCredentialStore() });
        using var outside = TempDirectory.With(new Dictionary<string, string> { ["secret.txt"] = "alice\ns3cret\n" });

        // A backslash is an escape in a profile, so the path is written with slashes, as OpenVPN profiles on Windows do.
        var outsidePath = outside.Resolve("secret.txt").Replace('\\', '/');
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["router.ovpn"] = $"client\nremote vpn.example.net 1194\nauth-user-pass {outsidePath}\n",
        });

        await app.RunAsync(async () =>
        {
            await app.Model.ImportFilesAsync([directory.Resolve("router.ovpn")]);

            var report = Assert.IsType<ImportReport>(app.Model.ImportReport);
            var failed = Assert.IsType<ImportOutcome.Failed>(report.Outcomes[0]);

            // The refused file is named, so the user knows which line of the profile to look at.
            Assert.Contains(outsidePath, failed.Message, StringComparison.Ordinal);
            Assert.Empty(app.Store.Profiles);
        });
    }

    // The fake backend takes any profile; the real parser is what refuses a file reference.
    [Fact]
    public async Task TheDaemonsReasonForRefusingAPkcs12ProfileIsShown()
    {
        if (Environment.GetEnvironmentVariable("PLAITWAY_REAL_DAEMON_TESTS") != "1")
        {
            Assert.Skip("needs the daemon's real engines; set PLAITWAY_REAL_DAEMON_TESTS=1 where they exist (macOS only so far)");
        }

        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Fake = false });
        using var directory = TempDirectory.With(new Dictionary<string, string>
        {
            ["router.ovpn"] = "client\nremote vpn.example.net 1194\npkcs12 client.p12\n",
        });

        await app.RunAsync(async () =>
        {
            await app.Model.ImportFilesAsync([directory.Resolve("router.ovpn")]);

            var report = Assert.IsType<ImportReport>(app.Model.ImportReport);
            Assert.True(report.NeedsAttention);
            Assert.Contains("pkcs12", Assert.IsType<ImportOutcome.Failed>(report.Outcomes[0]).Message, StringComparison.Ordinal);
            Assert.Empty(app.Store.Profiles);
        });
    }
}
