using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Tests.Support;
using Plaitway.AppCore.Text;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// The app runs <c>plaitwayd.exe</c> with administrator rights after one click on Yes. A copy of the app unzipped into a
/// folder of the user must not let any process of the user choose what that click runs: the file has to lie where only
/// administrators can write, or carry the publisher's signature.
/// </summary>
public sealed class HelperTrustTests(DaemonBinary binary)
{
    private const string AppDirectory = @"C:\Users\someone\Downloads\Plaitway\app";
    private const string NextToApp = AppDirectory + @"\plaitwayd.exe";
    private const string OneFolderUp = @"C:\Users\someone\Downloads\Plaitway\plaitwayd.exe";
    private const string Repository = @"C:\Users\someone\src\Plaitway";
    private const string RepositoryBuild = Repository + @"\bin\plaitwayd.exe";

    private static HelperLocator LocatorWith(FakeHelperTrust? trust, bool searchesRepository, params string[] existing) =>
        new(AppDirectory, path => existing.Contains(path), trust, searchesRepository);

    // the rule

    [Theory]
    [InlineData(FolderProtection.AdminOnly, FileSignature.NotSigned, HelperTrustVerdict.Trusted)]
    [InlineData(FolderProtection.AdminOnly, FileSignature.Invalid, HelperTrustVerdict.Trusted)]
    [InlineData(FolderProtection.WritableByUsers, FileSignature.Valid, HelperTrustVerdict.Trusted)]
    [InlineData(FolderProtection.NotLocalFixedDisk, FileSignature.Valid, HelperTrustVerdict.Trusted)]
    [InlineData(FolderProtection.Unknown, FileSignature.Valid, HelperTrustVerdict.Trusted)]
    [InlineData(FolderProtection.WritableByUsers, FileSignature.NotSigned, HelperTrustVerdict.FolderWritableByUsers)]
    [InlineData(FolderProtection.NotLocalFixedDisk, FileSignature.NotSigned, HelperTrustVerdict.FolderNotLocal)]
    [InlineData(FolderProtection.Unknown, FileSignature.NotSigned, HelperTrustVerdict.FolderNotChecked)]

    // A signature that is there and wrong says more than the folder does, wherever the file lies.
    [InlineData(FolderProtection.WritableByUsers, FileSignature.Invalid, HelperTrustVerdict.SignatureInvalid)]
    [InlineData(FolderProtection.NotLocalFixedDisk, FileSignature.Invalid, HelperTrustVerdict.SignatureInvalid)]
    [InlineData(FolderProtection.Unknown, FileSignature.Invalid, HelperTrustVerdict.SignatureInvalid)]
    [InlineData(FolderProtection.WritableByUsers, FileSignature.OtherPublisher, HelperTrustVerdict.SignatureOtherPublisher)]
    [InlineData(FolderProtection.Unknown, FileSignature.OtherPublisher, HelperTrustVerdict.SignatureOtherPublisher)]
    public void AFileIsTrustedInAnAdminOnlyFolderOrWithTheRightSignatureAndOtherwiseSaysWhy(FolderProtection folder, FileSignature signature, HelperTrustVerdict expected)
    {
        var trust = new FakeHelperTrust { Folder = folder, Signature = signature };

        Assert.Equal(expected, HelperTrustPolicy.Judge(NextToApp, trust));
    }

    [Fact]
    public void TheSignatureIsLookedAtOnlyWhereTheFolderDoesNotAlreadyDecide()
    {
        var adminOnly = new FakeHelperTrust { Folder = FolderProtection.AdminOnly };
        _ = HelperTrustPolicy.Judge(NextToApp, adminOnly);
        Assert.Empty(adminOnly.SignatureAsks);

        var writable = new FakeHelperTrust { Folder = FolderProtection.WritableByUsers };
        _ = HelperTrustPolicy.Judge(NextToApp, writable);
        Assert.Equal([$"{NextToApp}|{HelperTrustPolicy.ExpectedPublisher}"], writable.SignatureAsks);
    }

    // finding the file

    [Fact]
    public void AFileInAnAdminOnlyFolderIsReturnedAndNothingIsSaidAgainstIt()
    {
        var locator = LocatorWith(new FakeHelperTrust(), searchesRepository: false, NextToApp);

        Assert.Equal(new HelperResolution(NextToApp, null), locator.Resolve());
    }

    [Fact]
    public void AFileThatIsNotTrustedIsNotReturnedAndSaysWhatIsWrongWithIt()
    {
        var trust = new FakeHelperTrust { Folder = FolderProtection.WritableByUsers };
        var locator = LocatorWith(trust, searchesRepository: false, OneFolderUp);

        var found = locator.Resolve();

        Assert.Null(found.TrustedPath);
        Assert.Equal(new HelperDistrust(HelperTrustVerdict.FolderWritableByUsers, OneFolderUp), found.Distrust);
    }

    [Fact]
    public void WithNothingToFindNothingIsAskedOfWindows()
    {
        var trust = new FakeHelperTrust();

        Assert.Equal(new HelperResolution(null, null), LocatorWith(trust, searchesRepository: false).Resolve());
        Assert.Equal(0, trust.FolderAsked);
    }

    [Fact]
    public void TheFirstFileThatExistsIsTheOneJudgedAndALaterOneDoesNotTakeItsPlace()
    {
        // The copy next to the app is the one an installer put there; a copy further up is not a way round its verdict.
        var trust = new FakeHelperTrust { Folder = FolderProtection.WritableByUsers };
        var locator = LocatorWith(trust, searchesRepository: false, NextToApp, OneFolderUp);

        Assert.Equal(NextToApp, locator.Resolve().Distrust?.Path);
    }

    [Fact]
    public void AnAppThatWasNotGivenAJudgeTrustsNothing()
    {
        var locator = new HelperLocator(AppDirectory, path => path == NextToApp, searchesRepository: false);

        Assert.Equal(new HelperResolution(null, new HelperDistrust(HelperTrustVerdict.FolderNotChecked, NextToApp)), locator.Resolve());
    }

    [Fact]
    public void TheBuildFolderOfTheRepositoryIsFoundOnlyWhenAskedToLookThereAndIsTheDevelopersOwn()
    {
        // go.mod marks the repository root, which lies above the app's folder.
        var trust = new FakeHelperTrust { Folder = FolderProtection.WritableByUsers };
        string[] exists = [Repository + @"\go.mod", RepositoryBuild];
        var appBelowRepository = new Func<bool, HelperLocator>(searches => new HelperLocator(Repository + @"\windows\app", path => exists.Contains(path), trust, searches));

        var development = appBelowRepository(true).Resolve();
        Assert.Equal(new HelperResolution(RepositoryBuild, null), development);
        Assert.Equal(0, trust.FolderAsked);

        // A release build never looks there: a bin folder of any repository above the app is not where an installer puts it.
        Assert.Equal(new HelperResolution(null, null), appBelowRepository(false).Resolve());
    }

    [Fact]
    public void OnlyADebugBuildLooksInTheRepositoryByDefault()
    {
        string[] exists = [Repository + @"\go.mod", RepositoryBuild];
        var locator = new HelperLocator(Repository + @"\windows\app", path => exists.Contains(path));

        var found = locator.Resolve();

        Assert.Equal(HelperLocator.IsDevelopmentBuild ? RepositoryBuild : null, found.TrustedPath);
#if DEBUG
        Assert.True(HelperLocator.IsDevelopmentBuild);
#else
        Assert.False(HelperLocator.IsDevelopmentBuild);
#endif
    }

    // the installer

    private static (HelperInstaller Installer, FakeElevatedLauncher Launcher, FakeHelperTrust Trust) InstallerFor(FolderProtection folder)
    {
        var trust = new FakeHelperTrust { Folder = folder };
        var launcher = new FakeElevatedLauncher();
        var installer = new HelperInstaller(new FakeHelperService { State = HelperState.NotInstalled }, launcher, LocatorWith(trust, false, NextToApp));
        return (installer, launcher, trust);
    }

    [Fact]
    public async Task AFileThatIsNotTrustedIsNeverHandedToTheConsentPrompt()
    {
        var (installer, launcher, _) = InstallerFor(FolderProtection.WritableByUsers);

        Assert.False(installer.Status.HasExecutable);
        Assert.Equal(HelperTrustVerdict.FolderWritableByUsers, installer.Status.Distrust?.Verdict);

        var install = await Assert.ThrowsAsync<HelperMissingException>(() => installer.InstallAsync());
        _ = await Assert.ThrowsAsync<HelperMissingException>(() => installer.StartAsync());
        _ = await Assert.ThrowsAsync<HelperMissingException>(() => installer.ReinstallAsync());
        _ = await Assert.ThrowsAsync<HelperMissingException>(() => installer.UninstallAsync());

        Assert.Equal(new HelperDistrust(HelperTrustVerdict.FolderWritableByUsers, NextToApp), install.Distrust);
        Assert.Empty(launcher.Calls);
    }

    [Fact]
    public async Task AFileThatIsTrustedIsRunThroughTheConsentPrompt()
    {
        var (installer, launcher, _) = InstallerFor(FolderProtection.AdminOnly);

        Assert.True(installer.Status.HasExecutable);
        Assert.Null(installer.Status.Distrust);
        Assert.True(await installer.InstallAsync());

        Assert.Equal([$"{NextToApp} install -start"], launcher.Calls);
    }

    [Fact]
    public async Task TheFileIsJudgedAgainWhenTheCommandIsRunBecauseItMayHaveBeenReplacedSinceThePageWasDrawn()
    {
        var (installer, launcher, trust) = InstallerFor(FolderProtection.AdminOnly);
        Assert.True(installer.Status.HasExecutable);

        trust.Folder = FolderProtection.WritableByUsers;

        _ = await Assert.ThrowsAsync<HelperMissingException>(() => installer.InstallAsync());
        Assert.Empty(launcher.Calls);
        Assert.False(installer.Status.HasExecutable, "the failed attempt reads the state again");
    }

    [Fact]
    public async Task AFileThatBecomesTrustedIsUsedAfterTheStateIsReadAgain()
    {
        var (installer, launcher, trust) = InstallerFor(FolderProtection.NotLocalFixedDisk);
        Assert.Equal(HelperTrustVerdict.FolderNotLocal, installer.Status.Distrust?.Verdict);

        trust.Folder = FolderProtection.AdminOnly;
        installer.Refresh();

        Assert.True(await installer.InstallAsync());
        Assert.Single(launcher.Calls);
    }

    // what the user is told

    private static IEnumerable<HelperTrustVerdict> Refusals() => Enum.GetValues<HelperTrustVerdict>().Where(verdict => verdict != HelperTrustVerdict.Trusted);

    [Fact]
    public void AMissingHelperAndARefusedOneAreToldApartAndASetupWithNeitherStaysAsItWas()
    {
        var text = TestText.En;
        var missing = new DaemonSetup(SetupKind.HelperMissing).Content(text)!;

        Assert.Equal(text.HelperNotFound, missing.Title);
        Assert.Equal(text.TheHelperFileIsMissingFromThisCopyOfPlaitwayInstallPlaitwayAgain, missing.Detail);
        Assert.Equal(text.HelperNotFound, new DaemonSetup(SetupKind.HelperMissing).MenuStatus(text));

        var refused = new DaemonSetup(SetupKind.HelperMissing, Distrust: new HelperDistrust(HelperTrustVerdict.FolderWritableByUsers, NextToApp)).Content(text)!;

        Assert.Equal(text.HelperNotTrusted, refused.Title);
        Assert.NotEqual(missing.Title, refused.Title);
        Assert.Equal(SetupAction.Retry, refused.Primary);
        Assert.Null(refused.Secondary);
        Assert.Equal(text.HelperNotTrusted, new DaemonSetup(SetupKind.HelperMissing, Distrust: new HelperDistrust(HelperTrustVerdict.FolderNotLocal, NextToApp)).MenuStatus(text));
    }

    [Theory]
    [InlineData(TestText.English)]
    [InlineData(TestText.TraditionalChinese)]
    public void EveryRefusalNamesTheFileSaysWhyAndSaysWhatToDoInBothLanguages(string language)
    {
        var text = TestText.For(language);
        var reasons = new List<string>();

        foreach (var verdict in Refusals())
        {
            var detail = new HelperDistrust(verdict, NextToApp).Describe(text);
            var lines = detail.Split('\n');

            Assert.Equal(2, lines.Length);
            Assert.Contains(NextToApp, lines[0], StringComparison.Ordinal);
            Assert.Equal(text.InstallPlaitwayAgainInAFolderThatOnlyAdministratorsCanChange, lines[1]);
            reasons.Add(lines[0]);
        }

        // One precise reason per verdict, none of them shared; the folder checks that failed are not all "not found".
        Assert.Equal(reasons.Count, reasons.Distinct().Count());
    }

    [Fact]
    public void TheWordsOfARefusalAreTheSameInTheSetupPageTheMenuAndTheAlert()
    {
        var text = TestText.En;
        var distrust = new HelperDistrust(HelperTrustVerdict.SignatureOtherPublisher, NextToApp);

        Assert.Equal(distrust.Describe(text), new ErrorText(text).UserMessage(new HelperMissingException(distrust)));
        Assert.Equal(distrust.Describe(text), new DaemonSetup(SetupKind.HelperMissing, Distrust: distrust).Content(text)?.Detail);
        Assert.Equal(text.TheHelperFileIsMissingFromThisCopyOfPlaitwayInstallPlaitwayAgain, new ErrorText(text).UserMessage(new HelperMissingException()));
    }

    [Fact]
    public void TheSetupCarriesTheRefusalOfTheHelperFileOnlyWhileThereIsNoServiceToTalkTo()
    {
        var distrust = new HelperDistrust(HelperTrustVerdict.SignatureInvalid, NextToApp);
        SetupInputs Inputs(DaemonConnection connection, HelperState state) =>
            new(connection, null, new HelperStatus(state, null, distrust), null, "0.1.0", IsOverridden: false, IsSettling: false);

        Assert.Equal(new DaemonSetup(SetupKind.HelperMissing, Distrust: distrust), DaemonSetup.From(Inputs(DaemonConnection.Unavailable, HelperState.NotInstalled)));

        // A helper that something else installed may be about to answer; the file next to the app is beside the point then.
        Assert.Equal(DaemonSetup.Connecting, DaemonSetup.From(Inputs(DaemonConnection.Connecting, HelperState.NotInstalled)));
    }
    // through the model

    [Fact]
    public async Task ARefusedHelperIsReportedAsAnAlertWithItsReasonAndNoPromptIsShown()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            app.Trust.Folder = FolderProtection.WritableByUsers;
            app.Installer.Refresh();

            await app.Model.InstallHelperAsync();

            var distrust = new HelperDistrust(HelperTrustVerdict.FolderWritableByUsers, @"C:\Plaitway\app\plaitwayd.exe");
            Assert.Contains(distrust.Describe(app.Text), app.Model.Alert?.Message, StringComparison.Ordinal);
            Assert.Empty(app.Launcher.Calls);
        });
    }

    [Fact]
    public async Task AFileInAFolderOfTheUserIsAcceptedWhenItIsSignedByThePublisher()
    {
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { HelperState = HelperState.NotInstalled });
        await app.RunAsync(async () =>
        {
            app.Trust.Folder = FolderProtection.WritableByUsers;
            app.Trust.Signature = FileSignature.Valid;

            await app.Model.InstallHelperAsync();

            Assert.Null(app.Model.Alert);
            Assert.Equal([@"C:\Plaitway\app\plaitwayd.exe install -start"], app.Launcher.Calls);
        });
    }
}
