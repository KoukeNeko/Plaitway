using CommunityToolkit.Mvvm.ComponentModel;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.Client.Config;
using Plaitway.V1;

namespace Plaitway.AppCore.Editing;

/// <summary>Whether the text of a profile is on the screen.</summary>
public enum EditorPhase
{
    /// <summary>It is being read.</summary>
    Loading,

    /// <summary>It can be read and changed.</summary>
    Ready,

    /// <summary>It cannot be read: the person is not an administrator, or the daemon is away.</summary>
    Unavailable,
}

/// <summary>
/// The text of one profile while it is edited: what the editor shows, what the daemon holds, and what the daemon
/// said about the last attempt to store it. It lives in the app model, so that a half-made edit survives a visit to
/// another profile.
/// </summary>
/// <remarks>
/// The secrets (private keys, inline key blocks) are kept off the screen: the editor shows a placeholder in their
/// place until the person asks to see them, and <see cref="SecretMask"/> puts them back at save time.
/// </remarks>
/// <param name="profileId">The profile.</param>
/// <param name="kind">Its kind, which decides what a secret is.</param>
/// <param name="errors">Words for what goes wrong.</param>
/// <param name="uiText">The strings.</param>
public sealed partial class ProfileEditor(string profileId, ProfileKind kind, ErrorText errors, UiText uiText) : ObservableObject
{
    /// <summary>The text the daemon holds, secrets included, with its lines ended by line feeds alone.</summary>
    private string _stored = string.Empty;

    /// <summary>The daemon's text ends its lines with a carriage return and a line feed, which are put back when it is stored.</summary>
    private bool _storedUsesCarriageReturn;

    private SecretMask _mask = new(string.Empty, kind);

    /// <summary>The profile this edits.</summary>
    public string ProfileId { get; } = profileId;

    /// <summary>Its kind.</summary>
    public ProfileKind Kind { get; } = kind;

    /// <summary>Whether the text can be shown.</summary>
    [ObservableProperty]
    public partial EditorPhase Phase { get; private set; } = EditorPhase.Loading;

    /// <summary>Why the text cannot be read, while <see cref="Phase"/> says so.</summary>
    [ObservableProperty]
    public partial string UnavailableMessage { get; private set; } = string.Empty;

    /// <summary>What the editor shows and the person changes.</summary>
    [ObservableProperty]
    public partial string Text { get; set; } = string.Empty;

    /// <summary>The secrets are on the screen.</summary>
    [ObservableProperty]
    public partial bool ShowsSecrets { get; private set; }

    /// <summary>What the daemon refused in the last save; its line is a line of <see cref="Text"/>.</summary>
    [ObservableProperty]
    public partial ConfigDiagnostic? Diagnostic { get; private set; }

    /// <summary>What the daemon changed in the text it stored.</summary>
    [ObservableProperty]
    public partial IReadOnlyList<ImportWarning> Warnings { get; private set; } = [];

    /// <summary>The last save did not restart the profile, which is on: it runs with the old text.</summary>
    [ObservableProperty]
    public partial bool RunsOldText { get; private set; }

    /// <summary>A save is under way.</summary>
    [ObservableProperty]
    public partial bool IsSaving { get; private set; }

    /// <summary>The text with the secrets back in it; null while a placeholder stands twice.</summary>
    private string? RestoredText => ShowsSecrets ? Text : TryRestore();

    /// <summary>Whether the person changed something. A text that cannot be restored is changed by definition.</summary>
    public bool IsDirty => Phase == EditorPhase.Ready && RestoredText != _stored;

    /// <summary>Reads the profile text, unless there are edits that a read would throw away.</summary>
    public async Task LoadAsync(ProfileStore store, CancellationToken cancellationToken = default)
    {
        if (IsDirty)
        {
            return;
        }

        try
        {
            Hold(await store.GetProfileContentAsync(ProfileId, cancellationToken));
            Diagnostic = null;
            Show(_stored);
            Phase = EditorPhase.Ready;
        }
        catch (OperationCanceledException)
        {
            // A read that was cancelled by leaving the page says nothing; the next visit reads again.
        }
        catch (Exception error)
        {
            // An edit in progress stays on screen; a failed first read says why.
            if (Phase != EditorPhase.Ready)
            {
                UnavailableMessage = errors.UserMessage(error);
                Phase = EditorPhase.Unavailable;
            }
        }
    }

    /// <summary>Shows the secrets, or hides them again with whatever the person typed in the meantime.</summary>
    public void ToggleSecrets()
    {
        if (Phase != EditorPhase.Ready)
        {
            return;
        }

        // A mark is a line of the text as it was shown, and showing or hiding a key block changes the lines.
        Diagnostic = null;
        if (ShowsSecrets)
        {
            HideSecretsKeepingEdits();
            return;
        }

        try
        {
            Text = _mask.Restore(Text).Text;
            ShowsSecrets = true;
        }
        catch (DuplicatedPlaceholderException duplicate)
        {
            // A secret cannot be shown in two places.
            Diagnostic = Duplicate(duplicate);
        }
    }

    /// <summary>
    /// Hides the secrets again, with the edits made while they were shown. The editor outlives the page, and the keys
    /// must not be on screen when the person comes back to it.
    /// </summary>
    public void HideSecrets()
    {
        if (ShowsSecrets)
        {
            ToggleSecrets();
        }
    }

    /// <summary>The profile connected again: it runs the text that is stored now.</summary>
    public void NoteRestart() => RunsOldText = false;

    /// <summary>Goes back to the text the daemon holds.</summary>
    public void Revert()
    {
        Show(_stored);
        Diagnostic = null;
    }

    /// <summary>
    /// Stores the text. Returns whether the daemon took it; when it refused the text, <see cref="Diagnostic"/> says why.
    /// Any other failure is thrown.
    /// </summary>
    /// <param name="store">Where to send it.</param>
    /// <param name="reconnect">Restarts the profile with the new text.</param>
    /// <param name="isOn">The profile is switched on, so that it keeps running with the old text unless restarted.</param>
    /// <param name="cancellationToken">Cancels the call.</param>
    public async Task<bool> SaveAsync(ProfileStore store, bool reconnect, bool isOn, CancellationToken cancellationToken = default)
    {
        if (Phase != EditorPhase.Ready || IsSaving)
        {
            return false;
        }

        SecretMask.Restoration? restoration = null;
        var content = Text;
        if (!ShowsSecrets)
        {
            try
            {
                restoration = _mask.Restore(Text);
                content = restoration.Text;
            }
            catch (DuplicatedPlaceholderException duplicate)
            {
                Diagnostic = Duplicate(duplicate);
                return false;
            }
        }

        IsSaving = true;
        try
        {
            var sent = LineEndings.Restore(content, _storedUsesCarriageReturn);
            var result = await store.UpdateProfileContentAsync(ProfileId, sent, reconnect, cancellationToken);
            Warnings = result.Warnings;
            RunsOldText = isOn && !reconnect;
            Diagnostic = null;

            // The daemon may have stripped something from what it stored: show what it holds.
            Hold(await ReadBackAsync(store, sent, cancellationToken));
            Show(_stored);
            return true;
        }
        catch (Exception error) when (ConfigDiagnostic.FromError(error) is not null)
        {
            var rejection = ConfigDiagnostic.FromError(error)!;
            Diagnostic = new ConfigDiagnostic(rejection.Line is { } line ? restoration?.DisplayLine(line) ?? line : null, rejection.Message);
            return false;
        }
        finally
        {
            IsSaving = false;
        }
    }

    partial void OnTextChanged(string value)
    {
        // A text box reports its line breaks as carriage returns; they are one kind of break here.
        var normalized = LineEndings.Normalize(value);
        if (normalized != value)
        {
            Text = normalized;
            return;
        }

        OnPropertyChanged(nameof(IsDirty));
    }

    partial void OnPhaseChanged(EditorPhase value) => OnPropertyChanged(nameof(IsDirty));

    partial void OnShowsSecretsChanged(bool value) => OnPropertyChanged(nameof(IsDirty));

    private void Hold(string stored)
    {
        _storedUsesCarriageReturn = LineEndings.UsesCarriageReturn(stored);
        _stored = LineEndings.Normalize(stored);
    }

    private string? TryRestore()
    {
        try
        {
            return _mask.Restore(Text).Text;
        }
        catch (DuplicatedPlaceholderException)
        {
            return null;
        }
    }

    private void HideSecretsKeepingEdits()
    {
        _mask = new SecretMask(Text, Kind);
        Text = _mask.DisplayText;
        ShowsSecrets = false;
    }

    /// <summary>Shows <paramref name="restored"/> as the editor's text, with the secrets hidden unless they are shown.</summary>
    private void Show(string restored)
    {
        if (ShowsSecrets)
        {
            Text = restored;
        }
        else
        {
            _mask = new SecretMask(restored, Kind);
            Text = _mask.DisplayText;
        }

        // What the daemon holds may have changed while the text did not (a save that stored exactly what was typed),
        // and then no change of Text tells the page that it is no longer edited.
        OnPropertyChanged(nameof(IsDirty));
    }

    private async Task<string> ReadBackAsync(ProfileStore store, string fallback, CancellationToken cancellationToken)
    {
        try
        {
            return await store.GetProfileContentAsync(ProfileId, cancellationToken);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            // The text was stored; what the daemon holds now is what was sent.
            return fallback;
        }
    }

    private ConfigDiagnostic Duplicate(DuplicatedPlaceholderException duplicate) =>
        new(duplicate.Line, uiText.SecretAppearsMoreThanOnce(duplicate.Number));
}
