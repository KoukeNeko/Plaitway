using Microsoft.Extensions.Logging;

namespace Plaitway.AppCore;

/// <summary>
/// What the app writes to its log. Profile ids and file names may be logged; credentials, profile text and keys never:
/// a message takes none of them.
/// </summary>
internal static partial class LogMessages
{
    [LoggerMessage(Level = LogLevel.Error, Message = "daemon unavailable")]
    public static partial void DaemonUnavailable(ILogger logger, Exception error);

    [LoggerMessage(Level = LogLevel.Error, Message = "could not read the daemon's version")]
    public static partial void DaemonVersionUnreadable(ILogger logger, Exception error);

    [LoggerMessage(Level = LogLevel.Error, Message = "could not save the credentials of {ProfileId}")]
    public static partial void CredentialsNotSaved(ILogger logger, Exception error, string profileId);

    [LoggerMessage(Level = LogLevel.Error, Message = "could not look up the saved credentials of {ProfileId}")]
    public static partial void CredentialsNotLookedUp(ILogger logger, Exception error, string profileId);

    [LoggerMessage(Level = LogLevel.Error, Message = "could not read the saved credentials of {ProfileId}")]
    public static partial void CredentialsNotRead(ILogger logger, Exception error, string profileId);

    [LoggerMessage(Level = LogLevel.Error, Message = "could not remove the saved credentials of {ProfileId}")]
    public static partial void CredentialsNotRemoved(ILogger logger, Exception error, string profileId);

    [LoggerMessage(Level = LogLevel.Error, Message = "could not answer the credential request of {ProfileId}")]
    public static partial void CredentialRequestNotAnswered(ILogger logger, Exception error, string profileId);

    [LoggerMessage(Level = LogLevel.Error, Message = "{Title}")]
    public static partial void CommandFailed(ILogger logger, Exception error, string title);

    [LoggerMessage(Level = LogLevel.Error, Message = "import of {Filename} failed")]
    public static partial void ImportFailed(ILogger logger, Exception error, string filename);

    [LoggerMessage(Level = LogLevel.Information, Message = "the stale route {Key} was already gone")]
    public static partial void StaleRouteAlreadyGone(ILogger logger, string key);
}
