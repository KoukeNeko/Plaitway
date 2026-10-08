using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Text;
using Plaitway.Client;
using Plaitway.Client.Import;

namespace Plaitway.AppCore.Presentation;

/// <summary>
/// What the user is told about a failure. The daemon's own text is shown only where it explains the input (a rejected
/// profile); everything else is worded here, in the language of the app.
/// </summary>
/// <param name="text">The strings.</param>
public sealed class ErrorText(UiText text)
{
    /// <summary>The words for <paramref name="error"/>.</summary>
    public string UserMessage(Exception error) => error switch
    {
        ProfileImportException import => Describe(import.Failure),
        HelperCommandFailedException failed => text.HelperCommandFailedWithExitCode(failed.ExitCode),
        HelperMissingException { Distrust: { } distrust } => distrust.Describe(text),
        HelperMissingException => text.TheHelperFileIsMissingFromThisCopyOfPlaitwayInstallPlaitwayAgain,
        _ => Describe(DaemonFailure.From(error)),
    };

    /// <summary>The words for a refusal or an outage of the daemon.</summary>
    public string Describe(DaemonFailure failure) => failure.Kind switch
    {
        DaemonFailureKind.PermissionDenied => text.AdministratorRequired,
        DaemonFailureKind.Unavailable => text.HelperUnavailable,
        DaemonFailureKind.ServerRefused => text.TheHelperSPipeBelongsToAnUnexpectedAccountPlaitwayDoesNotUseIt,
        DaemonFailureKind.NotFound => text.ProfileNotFound,
        _ => failure.Message,
    };

    /// <summary>The words for a profile file that could not be read for import; the file that was refused is named.</summary>
    public string Describe(ProfileImportFailure failure) => failure switch
    {
        ProfileImportFailure.Unreadable unreadable => text.CannotReadColon(unreadable.Path, unreadable.Reason),
        ProfileImportFailure.NotText notText => text.NotATextFileColon(notText.Path),
        ProfileImportFailure.TooLarge tooLarge => text.FileTooLargeColon(tooLarge.Path),
        ProfileImportFailure.MissingFile missing => text.FileForNotFoundColon(missing.Directive, missing.Path),
        ProfileImportFailure.NotKeyMaterial notKey => text.FileForHoldsNoCertificateOrKeyColon(notKey.Directive, notKey.Path),
        ProfileImportFailure.OutsideProfileDirectory outside => text.FileForIsOutsideTheProfileSFolderColon(outside.Directive, outside.Path),
        ProfileImportFailure.NotCredentials notCredentials => text.FileForAuthUserPassHoldsNoUserNameAndPasswordColon(notCredentials.Path),
        _ => failure.ToString() ?? string.Empty,
    };
}
