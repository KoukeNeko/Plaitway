using Plaitway.AppCore.Helper;

namespace Plaitway.AppCore.Tests;

/// <summary>
/// <see cref="WindowsHelperTrust"/> against the real file system and the real Authenticode of this PC. The anchors are what
/// every Windows has: Program Files is administrators-only, a folder below %TEMP% belongs to the user, and the programs of
/// Windows and of the .NET SDK are signed by Microsoft.
/// </summary>
public sealed class WindowsHelperTrustTests : IDisposable
{
    private const string SignedProgramPublisher = ".NET"; // the CN of the certificate of dotnet.exe
    private const string SystemWitness = "cmd.exe";
    private const string NetworkShareProgram = @"\\plaitway-test.invalid\share\plaitwayd.exe";
    private const int FirstByteOfTheSignedFileToChange = 4096;

    private readonly WindowsHelperTrust _trust = new();
    private readonly string _scratch = Directory.CreateTempSubdirectory("plaitway-trust-").FullName;

    public void Dispose() => Directory.Delete(_scratch, recursive: true);

    private static string SystemProgram => Path.Combine(Environment.SystemDirectory, SystemWitness);

    // The programs of Windows are signed through catalogs and carry no signature of their own, which is what the check
    // looks for; the host of the .NET SDK that builds this test has an embedded one.
    private static string SignedProgram => Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.ProgramFiles), "dotnet", "dotnet.exe");

    private string NewFile(string name, byte[] content)
    {
        var path = Path.Combine(_scratch, name);
        File.WriteAllBytes(path, content);
        return path;
    }

    // folders

    [Fact]
    public void AFileInTheSystemFolderIsAdministratorsOnly()
    {
        Assert.Equal(FolderProtection.AdminOnly, _trust.InspectFolder(SystemProgram));
    }

    [Fact]
    public void AFileInProgramFilesIsAdministratorsOnly()
    {
        Assert.True(File.Exists(SignedProgram), "the .NET SDK that builds this test is in Program Files");

        Assert.Equal(FolderProtection.AdminOnly, _trust.InspectFolder(SignedProgram));
    }

    [Fact]
    public void AFileInAFolderOfTheUserCanBeReplacedByTheUser()
    {
        var program = NewFile("plaitwayd.exe", [1, 2, 3]);

        Assert.Equal(FolderProtection.WritableByUsers, _trust.InspectFolder(program));
    }

    [Fact]
    public void ANetworkShareProtectsNothing()
    {
        Assert.Equal(FolderProtection.NotLocalFixedDisk, _trust.InspectFolder(NetworkShareProgram));
    }

    [Fact]
    public void AFileThatIsNotThereCannotBeChecked()
    {
        Assert.Equal(FolderProtection.Unknown, _trust.InspectFolder(Path.Combine(Environment.SystemDirectory, "plaitway-not-there", "plaitwayd.exe")));
    }

    [Fact]
    public void ALinkIsNotFollowed()
    {
        var target = Directory.CreateDirectory(Path.Combine(_scratch, "target")).FullName;
        var link = Path.Combine(_scratch, "link");
        try
        {
            Directory.CreateSymbolicLink(link, target);
        }
        catch (Exception error) when (error is UnauthorizedAccessException or IOException)
        {
            return; // a symbolic link needs a right this account may not have; the junction cases are the same check
        }

        File.WriteAllBytes(Path.Combine(target, "plaitwayd.exe"), [1]);

        Assert.NotEqual(FolderProtection.AdminOnly, _trust.InspectFolder(Path.Combine(link, "plaitwayd.exe")));
    }

    // signatures

    [Fact]
    public void AProgramSignedByItsPublisherIsValid()
    {
        Assert.Equal(FileSignature.Valid, _trust.InspectSignature(SignedProgram, SignedProgramPublisher));
    }

    [Fact]
    public void AValidSignatureOfAnotherPublisherIsNotPlaitways()
    {
        Assert.Equal(FileSignature.OtherPublisher, _trust.InspectSignature(SignedProgram, HelperTrustPolicy.ExpectedPublisher));
    }

    [Fact]
    public void AFileWithoutASignatureIsNotSigned()
    {
        var program = NewFile("plaitwayd.exe", [.. "MZ not really a program"u8]);

        Assert.Equal(FileSignature.NotSigned, _trust.InspectSignature(program, HelperTrustPolicy.ExpectedPublisher));
    }

    [Fact]
    public void ASignedFileThatWasChangedIsInvalid()
    {
        var bytes = File.ReadAllBytes(SignedProgram);
        bytes[FirstByteOfTheSignedFileToChange] ^= 0xFF;
        var tampered = NewFile("plaitwayd.exe", bytes);

        Assert.Equal(FileSignature.Invalid, _trust.InspectSignature(tampered, SignedProgramPublisher));
    }
}
