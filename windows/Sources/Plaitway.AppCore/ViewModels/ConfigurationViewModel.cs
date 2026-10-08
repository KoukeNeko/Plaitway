using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Editing;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;
using Plaitway.V1;

namespace Plaitway.AppCore.ViewModels;

/// <summary>
/// The profile's own text, to read and to change: the file the daemon keeps for it, with the secrets hidden until asked
/// for. The editor lives in the app model and outlives this page, so that an edit survives a visit to another profile.
/// </summary>
public sealed partial class ConfigurationViewModel : ObservableObject, IPageLifecycle, IDisposable
{
    private readonly AppModel _model;
    private readonly ProfileEditor _editor;
    private CancellationTokenSource? _loading;
    private Google.Protobuf.WellKnownTypes.Timestamp? _connectedSince;

    /// <summary>Makes the page of the profile <paramref name="profileId"/>.</summary>
    public ConfigurationViewModel(AppModel model, string profileId)
    {
        _model = model;
        ProfileId = profileId;
        _editor = model.EditorFor(model.Store.Find(profileId) ?? throw new ArgumentException("no such profile", nameof(profileId)));
        _connectedSince = ConnectedSinceNow();
        _editor.PropertyChanged += OnEditorChanged;
        _model.Store.PropertyChanged += OnStoreChanged;
    }

    /// <summary>The profile.</summary>
    public string ProfileId { get; }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>Whether the text is being read, can be edited, or cannot be read.</summary>
    public EditorPhase Phase => _editor.Phase;

    /// <summary>The text is being read.</summary>
    public bool IsLoading => _editor.Phase == EditorPhase.Loading;

    /// <summary>The text can be edited.</summary>
    public bool IsReady => _editor.Phase == EditorPhase.Ready;

    /// <summary>The text cannot be read.</summary>
    public bool IsUnavailable => _editor.Phase == EditorPhase.Unavailable;

    /// <summary>Why the text cannot be read.</summary>
    public string UnavailableMessage => _editor.UnavailableMessage;

    /// <summary>What the editor shows; the view writes it back as the user types.</summary>
    public string Content
    {
        get => _editor.Text;
        set => _editor.Text = value;
    }

    /// <summary>The user changed something and has not saved it.</summary>
    public bool IsDirty => _editor.IsDirty;

    /// <summary>The secrets are on the screen.</summary>
    public bool ShowsSecrets => _editor.ShowsSecrets;

    /// <summary>"Show Secrets" or "Hide Secrets": what pressing the button does.</summary>
    public string SecretsLabel => _editor.ShowsSecrets ? Text.HideSecrets : Text.ShowSecrets;

    /// <summary>A save is under way.</summary>
    public bool IsSaving => _editor.IsSaving;

    /// <summary>The text can be saved: it changed and no save is under way.</summary>
    public bool CanSave => _editor.IsDirty && !_editor.IsSaving;

    /// <summary>The profile is switched on, so a save may restart it.</summary>
    public bool OffersReconnect => _model.Store.Find(ProfileId)?.DesiredEnabled == true;

    /// <summary>The line of the text the daemon refused, 1-based; null when the refusal is of the text as a whole.</summary>
    public int? DiagnosticLine => _editor.Diagnostic?.Line;

    /// <summary>What the daemon refused, in its words; empty when it refused nothing.</summary>
    public string DiagnosticMessage => _editor.Diagnostic?.Message ?? string.Empty;

    /// <summary>"Line 4", or empty when the refusal is not tied to a line.</summary>
    public string DiagnosticPlace => _editor.Diagnostic?.Line is { } line ? Text.Line(line) : string.Empty;

    /// <summary>The daemon refused the last save.</summary>
    public bool HasDiagnostic => _editor.Diagnostic is not null;

    /// <summary>What the daemon removed or ignored in the text it stored; shown once the user has changed nothing since.</summary>
    public IReadOnlyList<string> Warnings => _editor.IsDirty ? [] : [.. _editor.Warnings.Select(warning => warning.Summary(Text))];

    /// <summary>There are warnings to show.</summary>
    public bool HasWarnings => Warnings.Count > 0;

    /// <summary>The profile runs the old text and the user has changed nothing since.</summary>
    public bool RunsOldText => _editor.RunsOldText && !_editor.IsDirty;

    /// <inheritdoc />
    public void Activate()
    {
        _loading?.Cancel();
        _loading?.Dispose();
        _loading = new CancellationTokenSource();
        _ = LoadAsync(_loading.Token);
    }

    /// <inheritdoc />
    public void Deactivate()
    {
        _loading?.Cancel();
        _loading?.Dispose();
        _loading = null;

        // The editor outlives the page, and the keys must not be on screen when the user comes back to it.
        _editor.HideSecrets();
    }

    /// <inheritdoc />
    public void Dispose()
    {
        Deactivate();
        _editor.PropertyChanged -= OnEditorChanged;
        _model.Store.PropertyChanged -= OnStoreChanged;
    }

    /// <summary>Shows the secrets, or hides them again.</summary>
    [RelayCommand]
    public void ToggleSecrets() => _editor.ToggleSecrets();

    /// <summary>Goes back to the text the daemon holds.</summary>
    [RelayCommand]
    public void Revert() => _editor.Revert();

    /// <summary>Stores the text; a running profile keeps the old text until it connects again.</summary>
    [RelayCommand(CanExecute = nameof(CanSave))]
    public Task SaveAsync() => SaveAsync(reconnect: false);

    /// <summary>Stores the text and restarts the profile with it.</summary>
    [RelayCommand(CanExecute = nameof(CanSave))]
    public Task SaveAndReconnectAsync() => SaveAsync(reconnect: true);

    private async Task SaveAsync(bool reconnect)
    {
        try
        {
            await _editor.SaveAsync(_model.Store, reconnect, _model.Store.Find(ProfileId)?.DesiredEnabled == true);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            _model.Report(error, AlertTitle.SaveFailed);
        }
    }

    private async Task LoadAsync(CancellationToken cancellationToken)
    {
        await _editor.LoadAsync(_model.Store, cancellationToken);
    }

    private void OnStoreChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName != nameof(Daemon.ProfileStore.Profiles))
        {
            return;
        }

        OnPropertyChanged(nameof(OffersReconnect));
        var since = ConnectedSinceNow();
        if (!Equals(since, _connectedSince))
        {
            // The profile connected again: it runs the text that is stored now.
            _connectedSince = since;
            _editor.NoteRestart();
        }
    }

    private Google.Protobuf.WellKnownTypes.Timestamp? ConnectedSinceNow() => _model.Store.Find(ProfileId)?.Status?.ConnectedSince;

    private void OnEditorChanged(object? sender, PropertyChangedEventArgs args)
    {
        foreach (var name in AffectedProperties(args.PropertyName))
        {
            OnPropertyChanged(name);
        }

        SaveCommand.NotifyCanExecuteChanged();
        SaveAndReconnectCommand.NotifyCanExecuteChanged();
    }

    private static IEnumerable<string> AffectedProperties(string? editorProperty) => editorProperty switch
    {
        nameof(ProfileEditor.Phase) => [nameof(Phase), nameof(IsLoading), nameof(IsReady), nameof(IsUnavailable), nameof(IsDirty), nameof(CanSave)],
        nameof(ProfileEditor.UnavailableMessage) => [nameof(UnavailableMessage)],
        nameof(ProfileEditor.Text) => [nameof(Content), nameof(IsDirty), nameof(CanSave), nameof(Warnings), nameof(HasWarnings), nameof(RunsOldText)],
        nameof(ProfileEditor.IsDirty) => [nameof(IsDirty), nameof(CanSave), nameof(Warnings), nameof(HasWarnings), nameof(RunsOldText)],
        nameof(ProfileEditor.ShowsSecrets) => [nameof(ShowsSecrets), nameof(SecretsLabel)],
        nameof(ProfileEditor.IsSaving) => [nameof(IsSaving), nameof(CanSave)],
        nameof(ProfileEditor.Diagnostic) => [nameof(DiagnosticLine), nameof(DiagnosticMessage), nameof(DiagnosticPlace), nameof(HasDiagnostic)],
        nameof(ProfileEditor.Warnings) => [nameof(Warnings), nameof(HasWarnings)],
        nameof(ProfileEditor.RunsOldText) => [nameof(RunsOldText)],
        _ => [],
    };
}
