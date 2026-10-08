using System.ComponentModel;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Platform;
using Plaitway.AppCore.Text;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.AppCore.Dialogs;

/// <summary>
/// Turns what the model is asking into dialogs, one at a time and in the order of what needs the user most: a profile
/// that waits for a password, then what a command did not manage, then the questions that come before something that
/// cannot be taken back, then what an import found. The model holds the state; this decides when the user sees it.
/// </summary>
public sealed class DialogCoordinator : IDisposable
{
    private readonly AppModel _model;
    private readonly IDialogService _dialogs;
    private readonly HashSet<CredentialPrompt> _answered = [];
    private bool _isShowing;

    /// <summary>Makes the coordinator; nothing is shown until <see cref="Start"/>.</summary>
    public DialogCoordinator(AppModel model, IDialogService dialogs)
    {
        _model = model;
        _dialogs = dialogs;
    }

    private UiText Text => _model.Text;

    /// <summary>Starts watching the model.</summary>
    public void Start()
    {
        _model.PropertyChanged += OnChanged;
        _model.Store.PropertyChanged += OnChanged;
        Pump();
    }

    /// <inheritdoc />
    public void Dispose()
    {
        _model.PropertyChanged -= OnChanged;
        _model.Store.PropertyChanged -= OnChanged;
    }

    private void OnChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName is nameof(ProfileStore.CredentialPrompts))
        {
            _answered.IntersectWith(_model.Store.CredentialPrompts);
        }

        if (args.PropertyName is nameof(ProfileStore.CredentialPrompts) or nameof(AppModel.Alert) or nameof(AppModel.PendingDeletion)
            or nameof(AppModel.IsConfirmingReinstall) or nameof(AppModel.IsConfirmingUninstall) or nameof(AppModel.ImportReport))
        {
            Pump();
        }
    }

    private void Pump()
    {
        if (!_isShowing && NextRequest() is not null)
        {
            _isShowing = true;
            _ = ShowAllAsync();
        }
    }

    private async Task ShowAllAsync()
    {
        try
        {
            while (NextRequest() is { } request)
            {
                await request();
            }
        }
        finally
        {
            _isShowing = false;
        }
    }

    /// <summary>The next dialog to show, or null when there is none.</summary>
    private Func<Task>? NextRequest()
    {
        if (_model.Store.CredentialPrompts.FirstOrDefault(prompt => !_answered.Contains(prompt)) is { } prompt)
        {
            return () => ShowCredentialsAsync(prompt);
        }

        if (_model.Alert is { } alert)
        {
            return () => ShowAlertAsync(alert);
        }

        if (_model.PendingDeletion is { } profileId)
        {
            return () => ConfirmDeletionAsync(profileId);
        }

        if (_model.IsConfirmingReinstall)
        {
            return ConfirmReinstallAsync;
        }

        if (_model.IsConfirmingUninstall)
        {
            return ConfirmUninstallAsync;
        }

        return _model.ImportReport is { } report ? () => ShowImportReportAsync(report) : null;
    }

    private async Task ShowCredentialsAsync(CredentialPrompt prompt)
    {
        _answered.Add(prompt);
        using var dialog = new CredentialPromptViewModel(_model, prompt);
        await _dialogs.ShowCredentialsAsync(dialog);
    }

    private async Task ShowAlertAsync(AppAlert alert)
    {
        await _dialogs.AskAsync(new ChoiceRequest(alert.Title, alert.Message, [new Choice(Text.Close, ChoiceRole.Cancel)]));
        if (_model.Alert == alert)
        {
            _model.Alert = null;
        }
    }

    private async Task ConfirmDeletionAsync(string profileId)
    {
        var name = _model.ProfileName(profileId) ?? string.Empty;
        var confirmed = await ConfirmAsync(Text.DeleteQuestion(name), Text.TheProfileAndItsSavedCredentialsAreRemoved, Text.Delete);

        // The dialog is over before the command runs: the id travels with the question.
        _model.PendingDeletion = null;
        if (confirmed)
        {
            await _model.DeleteAsync(profileId);
        }
    }

    private async Task ConfirmReinstallAsync()
    {
        var confirmed = await ConfirmAsync(Text.ReinstallHelperQuestion, Text.ConnectedProfilesDisconnect, Text.Reinstall);
        _model.IsConfirmingReinstall = false;
        if (confirmed)
        {
            await _model.ReinstallHelperAsync();
        }
    }

    private async Task ConfirmUninstallAsync()
    {
        // The helper keeps its state across an uninstall, so removing the service leaves the profiles (and their keys) on disk.
        var message = Text.ConnectedProfilesDisconnect + "\n" + Text.ProfilesStayInTheLogIn(HelperPaths.StateDirectory, HelperPaths.LogPath);
        var confirmed = await ConfirmAsync(Text.UninstallHelperQuestion, message, Text.Uninstall);
        _model.IsConfirmingUninstall = false;
        if (confirmed)
        {
            await _model.UninstallHelperAsync();
        }
    }

    private async Task ShowImportReportAsync(ImportReport report)
    {
        await _dialogs.ShowImportReportAsync(new ImportReportViewModel(report, Text));
        if (_model.ImportReport == report)
        {
            _model.ImportReport = null;
        }
    }

    private async Task<bool> ConfirmAsync(string title, string message, string confirmLabel)
    {
        var request = new ChoiceRequest(title, message, [new Choice(confirmLabel, ChoiceRole.Destructive), new Choice(Text.Cancel, ChoiceRole.Cancel)]);
        return await _dialogs.AskAsync(request) == 0;
    }
}

/// <summary>The two questions of Quit, as dialogs.</summary>
/// <param name="text">The strings.</param>
/// <param name="dialogs">Where they are shown.</param>
public sealed class QuitPrompts(UiText text, IDialogService dialogs) : IQuitPrompts
{
    /// <inheritdoc />
    public async Task<bool> ConfirmDiscardingEditsAsync()
    {
        var request = new ChoiceRequest(
            text.QuitWithUnsavedChangesQuestion,
            text.ChangesToAProfileSTextAreNotSaved,
            [new Choice(text.Quit, ChoiceRole.Destructive), new Choice(text.Cancel, ChoiceRole.Cancel)]);
        return await dialogs.AskAsync(request) == 0;
    }

    /// <inheritdoc />
    public async Task<QuitAnswer> AskAsync()
    {
        var request = new ChoiceRequest(
            text.QuitPlaitwayQuestion,
            text.ConnectedProfilesStayConnected,
            [new Choice(text.Quit), new Choice(text.DisconnectAllAndQuit), new Choice(text.Cancel, ChoiceRole.Cancel)]);
        return await dialogs.AskAsync(request) switch
        {
            0 => QuitAnswer.Quit,
            1 => QuitAnswer.DisconnectAndQuit,
            _ => QuitAnswer.Cancel,
        };
    }
}
