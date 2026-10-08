using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Dialogs;

/// <summary>What the daemon asked of the user for one profile.</summary>
internal sealed partial class CredentialDialog : ContentDialog
{
    public CredentialDialog(CredentialPromptViewModel viewModel)
    {
        ViewModel = viewModel;
        InitializeComponent();
        Opened += (_, _) => (ViewModel.AsksForUsername ? (Control)UsernameBox : SecretBox).Focus(FocusState.Programmatic);
    }

    public CredentialPromptViewModel ViewModel { get; }

    // The dialog closes with the button; what the user typed is sent, or given up, by the view model.
    private void OnPrimaryClick(ContentDialog sender, ContentDialogButtonClickEventArgs args) => _ = ViewModel.SubmitCommand.ExecuteAsync(null);

    private void OnCloseClick(ContentDialog sender, ContentDialogButtonClickEventArgs args) => _ = ViewModel.CancelCommand.ExecuteAsync(null);
}
