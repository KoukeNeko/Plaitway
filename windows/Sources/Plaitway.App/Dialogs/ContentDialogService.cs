using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.Dialogs;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Dialogs;

/// <summary>The window that dialogs belong to.</summary>
internal interface IDialogHost
{
    /// <summary>Brings the window forward, and returns the root of its content once it can show a dialog.</summary>
    Task<FrameworkElement> ShowAsync();
}

/// <summary>
/// <see cref="IDialogService"/> in the window: a dialog covers the window it belongs to, and a window holds one at a time,
/// which <see cref="DialogCoordinator"/> sees to. The window is brought forward first: a dialog that nobody can see is no question.
/// </summary>
/// <param name="window">Shows the window and gives the root of its content, which a dialog needs.</param>
internal sealed class ContentDialogService(IDialogHost window) : IDialogService, IDisposable
{
    /// <summary>The resource that bounds the width of a dialog; the three buttons of a question share it, and the longest label needs about 190.</summary>
    private const string MaxWidthResource = "ContentDialogMaxWidth";

    private const double ChoiceDialogMaxWidth = 640;

    // A window can show one dialog at a time; one that is asked for meanwhile (Quit, with a prompt open) waits for its turn.
    private readonly SemaphoreSlim _turn = new(1, 1);

    /// <inheritdoc />
    public void Dispose() => _turn.Dispose();

    /// <inheritdoc />
    public async Task<int> AskAsync(ChoiceRequest request)
    {
        await _turn.WaitAsync();
        try
        {
            return await AskInTurnAsync(request);
        }
        finally
        {
            _turn.Release();
        }
    }

    /// <inheritdoc />
    public async Task ShowCredentialsAsync(CredentialPromptViewModel prompt)
    {
        await _turn.WaitAsync();
        try
        {
            var root = await window.ShowAsync();
            var dialog = new CredentialDialog(prompt) { XamlRoot = root.XamlRoot, RequestedTheme = root.ActualTheme };

            // The daemon may stop asking while the dialog is open (the profile was disconnected from elsewhere).
            _ = prompt.Completion.ContinueWith(_ => dialog.DispatcherQueue.TryEnqueue(dialog.Hide), TaskScheduler.Default);
            await dialog.ShowAsync();
        }
        finally
        {
            _turn.Release();
        }
    }

    /// <inheritdoc />
    public async Task ShowImportReportAsync(ImportReportViewModel report)
    {
        await _turn.WaitAsync();
        try
        {
            var root = await window.ShowAsync();
            await new ImportReportDialog(report) { XamlRoot = root.XamlRoot, RequestedTheme = root.ActualTheme }.ShowAsync();
        }
        finally
        {
            _turn.Release();
        }
    }

    private async Task<int> AskInTurnAsync(ChoiceRequest request)
    {
        var root = await window.ShowAsync();
        var cancel = request.DismissIndex;
        var others = Enumerable.Range(0, request.Choices.Count).Where(index => index != cancel).ToList();
        var dialog = new ContentDialog
        {
            XamlRoot = root.XamlRoot,
            RequestedTheme = root.ActualTheme,
            Title = request.Title,
            Content = request.Message is { } message ? new TextBlock { Text = message, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true } : null,
            CloseButtonText = request.Choices[cancel].Label,
            DefaultButton = ContentDialogButton.Close,
        };
        dialog.Resources[MaxWidthResource] = ChoiceDialogMaxWidth;
        if (others.Count > 0)
        {
            dialog.PrimaryButtonText = request.Choices[others[0]].Label;
        }

        if (others.Count > 1)
        {
            dialog.SecondaryButtonText = request.Choices[others[1]].Label;
        }

        // The button that does what cannot be taken back is not the one Enter presses.
        if (others.Count > 0 && request.Choices[others[0]].Role == ChoiceRole.Default)
        {
            dialog.DefaultButton = ContentDialogButton.Primary;
        }

        return await dialog.ShowAsync() switch
        {
            ContentDialogResult.Primary => others[0],
            ContentDialogResult.Secondary => others[1],
            _ => cancel,
        };
    }
}
