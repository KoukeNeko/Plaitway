using Plaitway.AppCore.Text;

namespace Plaitway.AppCore.Helper;

/// <summary>How well the folder that holds a program is protected from standard users.</summary>
public enum FolderProtection
{
    /// <summary>On a local fixed disk, and no folder of the path can be changed by a standard user (the rule of <c>CheckAdminOnlyPath</c> in <c>cmd/plaitwayd</c>).</summary>
    AdminOnly,

    /// <summary>A standard user can add to or replace something in the path.</summary>
    WritableByUsers,

    /// <summary>On a network share, a removable disk or a disk Windows does not know to be fixed: its protection says nothing.</summary>
    NotLocalFixedDisk,

    /// <summary>The access lists could not be read.</summary>
    Unknown,
}

/// <summary>What Authenticode says about a file.</summary>
public enum FileSignature
{
    /// <summary>Signed, the signature checks out, and the publisher is the expected one.</summary>
    Valid,

    /// <summary>There is no signature.</summary>
    NotSigned,

    /// <summary>There is a signature and it does not check out: the file was changed, or the certificate is not trusted, expired or revoked.</summary>
    Invalid,

    /// <summary>The signature checks out and belongs to somebody else.</summary>
    OtherPublisher,
}

/// <summary>
/// What the app asks Windows before it lets a program run elevated. Both answers are about the file as it is now, so
/// the caller asks again at the moment it launches. The implementation does not throw: what it cannot find out it
/// reports as <see cref="FolderProtection.Unknown"/> or <see cref="FileSignature.Invalid"/>, which are refusals.
/// </summary>
public interface IHelperTrust
{
    /// <summary>Whether only administrators can change the folder of <paramref name="executablePath"/>, or anything above it.</summary>
    FolderProtection InspectFolder(string executablePath);

    /// <summary>The Authenticode signature of <paramref name="executablePath"/> against the publisher the app expects.</summary>
    FileSignature InspectSignature(string executablePath, string expectedPublisher);
}

/// <summary>Why a <c>plaitwayd.exe</c> that exists is not allowed to run elevated.</summary>
public enum HelperTrustVerdict
{
    /// <summary>It may run.</summary>
    Trusted,

    /// <summary>It is unsigned and a standard user can replace it.</summary>
    FolderWritableByUsers,

    /// <summary>It is unsigned and not on a local fixed disk.</summary>
    FolderNotLocal,

    /// <summary>It is unsigned and its folder could not be checked.</summary>
    FolderNotChecked,

    /// <summary>It has a signature that does not check out.</summary>
    SignatureInvalid,

    /// <summary>It is signed by somebody other than Plaitway.</summary>
    SignatureOtherPublisher,
}

/// <summary>A program that was found and refused.</summary>
/// <param name="Verdict">Why; never <see cref="HelperTrustVerdict.Trusted"/>.</param>
/// <param name="Path">Where it was found.</param>
public sealed record HelperDistrust(HelperTrustVerdict Verdict, string Path);

/// <summary>
/// The rule for running a program with administrator rights: a copy an ordinary program cannot have replaced, by
/// where it is or by who signed it. Without it, any process of the user could plant a <c>plaitwayd.exe</c> next to a
/// copy of the app that was unzipped into a folder of the user and have it run elevated after the user clicked Yes.
/// </summary>
public static class HelperTrustPolicy
{
    /// <summary>The publisher whose signature is accepted wherever the file is.</summary>
    public const string ExpectedPublisher = "Plaitway";

    /// <summary>
    /// Trusted when the folder is admin-only, or when the signature is valid. A signature in a folder that users can
    /// change is checked at one moment and the file launched at another: the folder is the stronger of the two.
    /// </summary>
    public static HelperTrustVerdict Judge(string executablePath, IHelperTrust trust)
    {
        var folder = trust.InspectFolder(executablePath);
        if (folder == FolderProtection.AdminOnly)
        {
            return HelperTrustVerdict.Trusted;
        }

        return trust.InspectSignature(executablePath, ExpectedPublisher) switch
        {
            FileSignature.Valid => HelperTrustVerdict.Trusted,
            FileSignature.Invalid => HelperTrustVerdict.SignatureInvalid,
            FileSignature.OtherPublisher => HelperTrustVerdict.SignatureOtherPublisher,
            _ => VerdictOfUnsignedIn(folder),
        };
    }

    private static HelperTrustVerdict VerdictOfUnsignedIn(FolderProtection folder) => folder switch
    {
        FolderProtection.WritableByUsers => HelperTrustVerdict.FolderWritableByUsers,
        FolderProtection.NotLocalFixedDisk => HelperTrustVerdict.FolderNotLocal,
        _ => HelperTrustVerdict.FolderNotChecked,
    };
}

/// <summary>The words for a refused helper.</summary>
public static class HelperDistrustText
{
    /// <summary>What was refused and what to do about it: two lines, the reason and the way out.</summary>
    public static string Describe(this HelperDistrust distrust, UiText text) =>
        string.Join('\n', Reason(distrust, text), text.InstallPlaitwayAgainInAFolderThatOnlyAdministratorsCanChange);

    private static string Reason(HelperDistrust distrust, UiText text) => distrust.Verdict switch
    {
        HelperTrustVerdict.FolderWritableByUsers => text.TheHelperFileIsInAFolderThatStandardUsersCanChangeColon(distrust.Path),
        HelperTrustVerdict.FolderNotLocal => text.TheHelperFileIsNotOnALocalDiskColon(distrust.Path),
        HelperTrustVerdict.SignatureInvalid => text.TheSignatureOfTheHelperFileIsNotValidColon(distrust.Path),
        HelperTrustVerdict.SignatureOtherPublisher => text.TheHelperFileIsSignedByAnUnexpectedPublisherColon(distrust.Path),
        _ => text.TheFolderOfTheHelperFileCouldNotBeCheckedColon(distrust.Path),
    };
}

/// <summary>The trust of an app that was not given one: nothing is checked, so nothing outside a development build is trusted.</summary>
internal sealed class UncheckedHelperTrust : IHelperTrust
{
    /// <summary>The one instance.</summary>
    public static UncheckedHelperTrust Instance { get; } = new();

    /// <inheritdoc />
    public FolderProtection InspectFolder(string executablePath) => FolderProtection.Unknown;

    /// <inheritdoc />
    public FileSignature InspectSignature(string executablePath, string expectedPublisher) => FileSignature.NotSigned;
}
