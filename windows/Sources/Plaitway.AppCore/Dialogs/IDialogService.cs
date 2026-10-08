using Plaitway.AppCore.ViewModels;

namespace Plaitway.AppCore.Dialogs;

/// <summary>What pressing a button of a dialog means; the view gives it the look of the default, the destructive or the cancelling button.</summary>
public enum ChoiceRole
{
    /// <summary>The button the Enter key presses.</summary>
    Default,

    /// <summary>The button that does what cannot be taken back; it is not the one Enter presses.</summary>
    Destructive,

    /// <summary>The button the Escape key presses and a dismissal counts as.</summary>
    Cancel,
}

/// <summary>A button of a dialog.</summary>
/// <param name="Label">Names the action, never a reply: "Delete", not "Yes".</param>
/// <param name="Role">What kind of button it is.</param>
public sealed record Choice(string Label, ChoiceRole Role = ChoiceRole.Default);

/// <summary>A question for the user, with the buttons that answer it.</summary>
/// <param name="Title">The question, or what happened.</param>
/// <param name="Message">What the user needs to know to decide; null when the title says it.</param>
/// <param name="Choices">The buttons, in order; at most one of them cancels.</param>
public sealed record ChoiceRequest(string Title, string? Message, IReadOnlyList<Choice> Choices)
{
    /// <summary>The index of the button that cancels, or the last button when none does.</summary>
    public int DismissIndex
    {
        get
        {
            var cancel = Choices.ToList().FindIndex(choice => choice.Role == ChoiceRole.Cancel);
            return cancel >= 0 ? cancel : Choices.Count - 1;
        }
    }
}

/// <summary>
/// The dialogs of the app. The window shows them (one at a time: a window can hold one); the tests answer them. A dialog
/// that the user does not answer is a cancellation, and so is one that closes because what it asked about is gone.
/// </summary>
public interface IDialogService
{
    /// <summary>Asks, and returns the index of the button the user pressed; the cancelling one when the dialog is dismissed.</summary>
    Task<int> AskAsync(ChoiceRequest request);

    /// <summary>Shows the credential dialog until <see cref="CredentialPromptViewModel.Completion"/> completes.</summary>
    Task ShowCredentialsAsync(CredentialPromptViewModel prompt);

    /// <summary>Shows what came of an import until the user closes it.</summary>
    Task ShowImportReportAsync(ImportReportViewModel report);
}
