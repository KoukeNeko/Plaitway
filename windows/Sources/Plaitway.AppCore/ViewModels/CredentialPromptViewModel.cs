using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Text;

namespace Plaitway.AppCore.ViewModels;

/// <summary>What the daemon asked of the user for one profile, and what the user typed. It is over when it is answered, cancelled or gone.</summary>
public sealed partial class CredentialPromptViewModel : ObservableObject, IDisposable
{
    private readonly AppModel _model;
    private readonly TaskCompletionSource _completion = new(TaskCreationOptions.RunContinuationsAsynchronously);

    /// <summary>Makes the dialog for <paramref name="prompt"/>.</summary>
    public CredentialPromptViewModel(AppModel model, CredentialPrompt prompt)
    {
        _model = model;
        Prompt = prompt;
        _model.Store.PropertyChanged += OnStoreChanged;
    }

    /// <summary>The strings.</summary>
    public UiText Text => _model.Text;

    /// <summary>What was asked.</summary>
    public CredentialPrompt Prompt { get; }

    /// <summary>The profile that asked.</summary>
    public string ProfileId => Prompt.ProfileId;

    /// <summary>The name of the profile that asked.</summary>
    public string ProfileName => _model.ProfileName(Prompt.ProfileId) ?? Prompt.ProfileId;

    /// <summary>Credentials, or Key passphrase.</summary>
    public string Title => Prompt.AsksForUsername ? Text.Credentials : Text.KeyPassphrase;

    /// <summary>A user name is asked for, besides the secret.</summary>
    public bool AsksForUsername => Prompt.AsksForUsername;

    /// <summary>The label of the secret's field: Password, or Passphrase.</summary>
    public string SecretLabel => Prompt.AsksForUsername ? Text.Password : Text.Passphrase;

    /// <summary>Why the daemon refused what was sent before; empty on a first request.</summary>
    public string RejectionMessage => !Prompt.Rejected ? string.Empty : Prompt.Message.Length == 0 ? Text.CredentialsRejected : Prompt.Message;

    /// <summary>The daemon refused what was sent before.</summary>
    public bool IsRejected => Prompt.Rejected;

    /// <summary>Completes when the dialog has to close: the user answered or cancelled, or the daemon no longer asks.</summary>
    public Task Completion => _completion.Task;

    /// <summary>What the user typed as the user name.</summary>
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(CanSubmit))]
    [NotifyCanExecuteChangedFor(nameof(SubmitCommand))]
    public partial string Username { get; set; } = string.Empty;

    /// <summary>What the user typed as the secret.</summary>
    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(CanSubmit))]
    [NotifyCanExecuteChangedFor(nameof(SubmitCommand))]
    public partial string Password { get; set; } = string.Empty;

    /// <summary>There is something to send: a secret, and a user name when one is asked for.</summary>
    public bool CanSubmit => Password.Length > 0 && (!Prompt.AsksForUsername || Username.Length > 0);

    /// <inheritdoc />
    public void Dispose() => _model.Store.PropertyChanged -= OnStoreChanged;

    /// <summary>Sends what the user typed.</summary>
    [RelayCommand(CanExecute = nameof(CanSubmit))]
    public async Task SubmitAsync()
    {
        var user = Prompt.AsksForUsername ? Username : string.Empty;
        var secret = Password;

        // The dialog goes away with the prompt; the secret does not stay in the view model.
        Password = string.Empty;
        _completion.TrySetResult();
        await _model.SubmitCredentialsAsync(Prompt.ProfileId, user, secret);
    }

    /// <summary>The user gave up: the profile stops connecting.</summary>
    [RelayCommand]
    public async Task CancelAsync()
    {
        Password = string.Empty;
        _completion.TrySetResult();
        await _model.CancelCredentialsAsync(Prompt.ProfileId);
    }

    private void OnStoreChanged(object? sender, PropertyChangedEventArgs args)
    {
        if (args.PropertyName == nameof(ProfileStore.CredentialPrompts)
            && !_model.Store.CredentialPrompts.Any(candidate => candidate.ProfileId == Prompt.ProfileId))
        {
            _completion.TrySetResult();
        }
    }
}
