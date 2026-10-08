using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.Client;

namespace Plaitway.AppCore.Helper;

/// <summary>What the user can press when the helper is not ready.</summary>
public enum SetupAction
{
    /// <summary>Register the service and start it (elevated).</summary>
    InstallHelper,

    /// <summary>Start the registered service (elevated).</summary>
    StartHelper,

    /// <summary>Look again; the helper may have come up since.</summary>
    Retry,

    /// <summary>Register the service again, which drops every tunnel (elevated).</summary>
    ReinstallHelper,
}

/// <summary>What the window shows instead of the profiles while the helper is not ready: one thing to do about it.</summary>
/// <param name="Icon">The glyph above the title.</param>
/// <param name="Title">What is wrong.</param>
/// <param name="Detail">What the user needs to know to act; null when the title says it.</param>
/// <param name="ShowsProgress">Something is under way.</param>
/// <param name="Primary">The action to take, or null when there is none.</param>
/// <param name="Secondary">An action for when the first does not help.</param>
public sealed record SetupContent(StatusIcon Icon, string Title, string? Detail, bool ShowsProgress, SetupAction? Primary, SetupAction? Secondary)
{
    /// <summary>The label of an action; one label per action everywhere.</summary>
    public static string Label(SetupAction action, UiText text) => action switch
    {
        SetupAction.InstallHelper => text.InstallHelper,
        SetupAction.StartHelper => text.StartHelper,
        SetupAction.Retry => text.Retry,
        _ => text.ReinstallHelper,
    };
}

/// <summary>The words for each state of the helper.</summary>
public static class SetupPresentation
{
    /// <summary>The state as the tray menu's disabled first line says it; null when there is nothing to say.</summary>
    public static string? MenuStatus(this DaemonSetup setup, UiText text) => setup.Kind switch
    {
        SetupKind.Ready => null,
        SetupKind.VersionMismatch => text.HelperOutOfDate,
        SetupKind.Connecting => text.Connecting,
        SetupKind.NeedsInstall => text.HelperNotInstalled,
        SetupKind.Stopped => text.HelperStopped,
        SetupKind.HelperMissing => text.HelperNotFound,
        _ => text.HelperUnavailable,
    };

    /// <summary>The window's content; null while the profiles can be shown.</summary>
    public static SetupContent? Content(this DaemonSetup setup, UiText text) => setup.Kind switch
    {
        SetupKind.Ready or SetupKind.VersionMismatch => null,
        SetupKind.Connecting => new SetupContent(StatusIcon.Idle, text.Connecting, null, ShowsProgress: true, null, null),
        SetupKind.NeedsInstall => new SetupContent(
            StatusIcon.Idle, text.HelperNotInstalled, text.TheHelperManagesRoutesAndDNSWithAdministratorRightsWindowsAsksForConfirmationOnce, false, SetupAction.InstallHelper, null),
        SetupKind.Stopped => new SetupContent(StatusIcon.Idle, text.HelperStopped, null, false, SetupAction.StartHelper, null),
        SetupKind.HelperMissing => new SetupContent(
            StatusIcon.Unavailable, text.HelperNotFound, text.TheHelperFileIsMissingFromThisCopyOfPlaitwayInstallPlaitwayAgain, false, SetupAction.Retry, null),
        SetupKind.NotResponding => NotResponding(setup, text),
        _ => new SetupContent(StatusIcon.Unavailable, text.HelperUnavailable, text.StartPlaitwaydAndSetPLAITWAYSOCKET, false, SetupAction.Retry, null),
    };

    private static SetupContent NotResponding(DaemonSetup setup, UiText text)
    {
        var cause = setup.Cause ?? DaemonFailureKind.Unavailable;
        var detail = cause switch
        {
            DaemonFailureKind.PermissionDenied => text.ThisAccountMayNotUseTheHelper,
            DaemonFailureKind.ServerRefused => text.TheHelperSPipeBelongsToAnUnexpectedAccountPlaitwayDoesNotUseIt,

            // The service is fine; the log is the only trace of why the daemon does not run.
            _ => text.RunningNotAnswering + "\n" + text.HelperLogColon(HelperPaths.LogPath),
        };

        // Registering again helps only when the service itself is what does not answer.
        var secondary = cause == DaemonFailureKind.Unavailable ? SetupAction.ReinstallHelper : (SetupAction?)null;
        return new SetupContent(StatusIcon.Unavailable, text.HelperUnavailable, detail, false, SetupAction.Retry, secondary);
    }
}
