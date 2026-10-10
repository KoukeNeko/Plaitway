using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore;

/// <summary>What a failed command is called in the alert.</summary>
public enum AlertTitle
{
    /// <summary>Importing profile files.</summary>
    ImportFailed,

    /// <summary>Deleting a profile.</summary>
    DeleteFailed,

    /// <summary>Changing the priority order.</summary>
    ReorderFailed,

    /// <summary>Saving a name, a setting or the text of a profile.</summary>
    SaveFailed,

    /// <summary>Connecting or disconnecting.</summary>
    ConnectFailed,

    /// <summary>Installing, starting, reinstalling or uninstalling the helper.</summary>
    HelperFailed,

    /// <summary>Changing the start with Windows.</summary>
    LaunchAtLoginFailed,

    /// <summary>Keeping the language chosen for the next start.</summary>
    LanguageNotSaved,

    /// <summary>Resyncing the daemon.</summary>
    ResyncFailed,

    /// <summary>Removing a stale route.</summary>
    RemoveFailed,
}

/// <summary>Something a command did not manage, for an alert.</summary>
/// <param name="Id">Tells one alert from the next, so that a new one with the same words is shown again.</param>
/// <param name="Title">What was attempted, as a failure.</param>
/// <param name="Message">What happened and what to do; may be several lines.</param>
public sealed record AppAlert(Guid Id, string Title, string Message)
{
    /// <summary>The title of an alert about <paramref name="title"/>.</summary>
    public static string TitleText(AlertTitle title, UiText text) => title switch
    {
        AlertTitle.ImportFailed => text.ImportFailed,
        AlertTitle.DeleteFailed => text.DeleteFailed,
        AlertTitle.ReorderFailed => text.ReorderFailed,
        AlertTitle.SaveFailed => text.SaveFailed,
        AlertTitle.ConnectFailed => text.ConnectFailed,
        AlertTitle.HelperFailed => text.HelperFailed,
        AlertTitle.LaunchAtLoginFailed => text.LaunchAtLoginFailed,
        AlertTitle.LanguageNotSaved => text.LanguageNotSaved,
        AlertTitle.ResyncFailed => text.ResyncFailed,
        _ => text.RemoveFailed,
    };
}

/// <summary>What came of importing one file.</summary>
/// <param name="Filename">The file's name, without its folder.</param>
public abstract record ImportOutcome(string Filename)
{
    /// <summary>The daemon stored the profile.</summary>
    /// <param name="Filename">The file.</param>
    /// <param name="Name">The name the profile has.</param>
    /// <param name="Warnings">What the daemon removed or ignored in it.</param>
    public sealed record Imported(string Filename, string Name, IReadOnlyList<ImportWarning> Warnings) : ImportOutcome(Filename);

    /// <summary>The file was refused.</summary>
    /// <param name="Filename">The file.</param>
    /// <param name="Message">Why, in words.</param>
    public sealed record Failed(string Filename, string Message) : ImportOutcome(Filename);
}

/// <summary>What came of importing the files the user picked or dropped.</summary>
/// <param name="Outcomes">One per file, in the order they were given.</param>
public sealed record ImportReport(IReadOnlyList<ImportOutcome> Outcomes)
{
    /// <summary>Worth a dialog: a file failed or the daemon changed something in it.</summary>
    public bool NeedsAttention => Outcomes.Any(outcome => outcome is ImportOutcome.Failed or ImportOutcome.Imported { Warnings.Count: > 0 });
}
