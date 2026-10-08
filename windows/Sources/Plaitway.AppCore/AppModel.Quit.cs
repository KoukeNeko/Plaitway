using Plaitway.AppCore.Platform;

namespace Plaitway.AppCore;

// Quitting. The helper keeps the profiles connected when the app is gone, which is not what everyone expects of Quit:
// with profiles switched on, the user chooses.
public sealed partial class AppModel
{
    /// <summary>Decides whether the app may end now, asking the user what needs asking.</summary>
    /// <param name="prompts">The questions, shown as dialogs.</param>
    /// <param name="isPoweringOff">Windows is signing out or shutting down: nothing may wait for an answer.</param>
    /// <param name="cancellationToken">Cancels the disconnecting.</param>
    public async Task<QuitDecision> RequestQuitAsync(IQuitPrompts prompts, bool isPoweringOff, CancellationToken cancellationToken = default)
    {
        // Profile text that was changed and not saved is lost with the app.
        if (!isPoweringOff && HasUnsavedEdits && !await prompts.ConfirmDiscardingEditsAsync())
        {
            return new QuitDecision(CanQuit: false, ShowWindow: true);
        }

        if (isPoweringOff || SwitchedOnProfiles.Count == 0)
        {
            return new QuitDecision(CanQuit: true, ShowWindow: false);
        }

        switch (await prompts.AskAsync())
        {
            case QuitAnswer.Quit:
                return new QuitDecision(CanQuit: true, ShowWindow: false);
            case QuitAnswer.DisconnectAndQuit:
                var allOff = await DisconnectAllAsync(cancellationToken);

                // A profile that stays on is reported in the window.
                return new QuitDecision(allOff, ShowWindow: !allOff);
            default:
                return new QuitDecision(CanQuit: false, ShowWindow: false);
        }
    }
}
