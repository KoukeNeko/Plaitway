using Grpc.Core;
using Microsoft.Extensions.Logging;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.AppCore.Daemon;

/// <summary>A profile that waits for the user to type what the daemon asked for.</summary>
/// <param name="ProfileId">The profile that asked.</param>
/// <param name="Kind">What it asked for.</param>
/// <param name="Rejected">The daemon refused what was sent before.</param>
/// <param name="Message">The daemon's reason for a refusal, which may be empty.</param>
public sealed record CredentialPrompt(string ProfileId, CredentialKind Kind, bool Rejected, string Message)
{
    /// <summary>A key passphrase has no user name.</summary>
    public bool AsksForUsername => Kind != CredentialKind.KeyPassphrase;
}

// Credential requests. A profile that asks is answered from the credential store when it has an answer, so that the
// user types each password once. The prompt appears when there is none, and again when the daemon refuses what was sent.
public sealed partial class ProfileStore
{
    private static readonly CredentialKind[] SavedKinds = [CredentialKind.UserPassword, CredentialKind.KeyPassphrase];

    /// <summary>Profiles that were sent credentials during the current connection attempt: asking again means the daemon refused them.</summary>
    private readonly HashSet<string> _answered = [];

    /// <summary>Profiles whose credential request is being looked up in the credential store.</summary>
    private readonly HashSet<string> _resolving = [];

    /// <summary>Sends what the user typed, and keeps it for the next request.</summary>
    /// <exception cref="RpcException">The profile is not waiting for credentials (FailedPrecondition), or the daemon refused the call.</exception>
    public async Task ProvideCredentialsAsync(string profileId, string username, string password, CancellationToken cancellationToken = default)
    {
        var prompt = CredentialPrompts.FirstOrDefault(candidate => candidate.ProfileId == profileId)
            ?? throw new RpcException(new Status(StatusCode.FailedPrecondition, "the profile is not waiting for credentials"));
        var credentials = new Credentials(username, password);
        await SaveCredentialsAsync(credentials, profileId, prompt.Kind, cancellationToken);
        await SendAsync(credentials, profileId, prompt.Kind, cancellationToken);
        RemovePrompt(profileId);
    }

    /// <summary>The user gave up on the prompt: stop connecting the profile.</summary>
    public async Task CancelCredentialsAsync(string profileId, CancellationToken cancellationToken = default)
    {
        RemovePrompt(profileId);
        _answered.Remove(profileId);
        await SetEnabledAsync(profileId, enabled: false, cancellationToken);
    }

    /// <summary>Whether the credential store holds an answer for the profile.</summary>
    public async Task<bool> HasSavedCredentialsAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default)
    {
        try
        {
            return await credentialStore.ContainsAsync(profileId, kind, cancellationToken);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            LogMessages.CredentialsNotLookedUp(log, error, profileId);
            return false;
        }
    }

    /// <summary>Forgets every answer saved for the profile.</summary>
    public async Task ForgetSavedCredentialsAsync(string profileId, CancellationToken cancellationToken = default)
    {
        foreach (var kind in SavedKinds)
        {
            await RemoveSavedCredentialsAsync(profileId, kind, cancellationToken);
        }
    }

    /// <summary>Keeps an answer for the next request. Connecting matters more than remembering, so a failure is logged and the next request asks again.</summary>
    private async Task SaveCredentialsAsync(Credentials credentials, string profileId, CredentialKind kind, CancellationToken cancellationToken)
    {
        try
        {
            await credentialStore.SaveAsync(credentials, profileId, kind, cancellationToken);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            LogMessages.CredentialsNotSaved(log, error, profileId);
        }
    }

    /// <summary>Called for every profile the daemon reports.</summary>
    private void ReviewCredentialRequest(Profile profile)
    {
        if (profile.State != ProfileState.AwaitingCredentials)
        {
            RemovePrompt(profile.Id);

            // CONNECTING is the daemon checking what was sent; only an attempt that ended starts the next one with a clean slate.
            if (profile.State is ProfileState.Connected or ProfileState.Disconnected or ProfileState.Failed)
            {
                _answered.Remove(profile.Id);
            }

            return;
        }

        var kind = RequestedKind(profile);
        if (CredentialPrompts.Any(prompt => prompt.ProfileId == profile.Id && prompt.Kind == kind))
        {
            return;
        }

        // The credential store may be slow to answer; one lookup per profile at a time.
        if (!_resolving.Add(profile.Id))
        {
            return;
        }

        var rejected = _answered.Contains(profile.Id) || profile.LastError.Length > 0;
        _ = ResolveAndReleaseAsync(profile, kind, rejected);
    }

    private void ForgetCredentialsOf(string profileId)
    {
        RemovePrompt(profileId);
        _answered.Remove(profileId);
        _ = ForgetSavedCredentialsInBackgroundAsync(profileId);
    }

    private void DiscardPromptsOfRemovedProfiles()
    {
        var existing = Profiles.Select(profile => profile.Id).ToHashSet();
        CredentialPrompts = [.. CredentialPrompts.Where(prompt => existing.Contains(prompt.ProfileId))];
    }

    private void RemovePrompt(string profileId)
    {
        if (CredentialPrompts.Any(prompt => prompt.ProfileId == profileId))
        {
            CredentialPrompts = [.. CredentialPrompts.Where(prompt => prompt.ProfileId != profileId)];
        }
    }

    private static CredentialKind RequestedKind(Profile profile) => profile.CredentialRequest?.Kind ?? CredentialKind.Unspecified;

    private async Task ForgetSavedCredentialsInBackgroundAsync(string profileId)
    {
        try
        {
            await ForgetSavedCredentialsAsync(profileId, _lifetime.Token);
        }
        catch (OperationCanceledException)
        {
            // The store is stopping; the daemon's state does not need this.
        }
    }

    private async Task ResolveAndReleaseAsync(Profile profile, CredentialKind kind, bool rejected)
    {
        try
        {
            await ResolveRequestAsync(profile, kind, rejected);
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested)
        {
            // The store is stopping.
        }
        finally
        {
            _resolving.Remove(profile.Id);
        }
    }

    /// <summary>Answers the request from the credential store, or asks the user.</summary>
    private async Task ResolveRequestAsync(Profile profile, CredentialKind kind, bool rejected)
    {
        if (rejected)
        {
            await RemoveSavedCredentialsAsync(profile.Id, kind, _lifetime.Token);
            _answered.Remove(profile.Id);
            ShowPrompt(profile, rejected: true);
            return;
        }

        var saved = await SavedCredentialsAsync(profile.Id, kind);

        // The credential store took its time; the profile may have been answered, stopped or deleted meanwhile.
        if (Find(profile.Id) is not { State: ProfileState.AwaitingCredentials } current)
        {
            return;
        }

        if (saved is null)
        {
            ShowPrompt(current, rejected: false);
            return;
        }

        try
        {
            await SendAsync(saved, profile.Id, kind, _lifetime.Token);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            // Not a refusal of the credentials: the daemon could not take them.
            LogMessages.CredentialRequestNotAnswered(log, error, profile.Id);
            if (Find(profile.Id)?.State == ProfileState.AwaitingCredentials)
            {
                ShowPrompt(current, rejected: false);
            }
        }
    }

    private async Task SendAsync(Credentials credentials, string profileId, CredentialKind kind, CancellationToken cancellationToken)
    {
        _answered.Add(profileId);
        try
        {
            _ = await api.ProvideCredentialsAsync(profileId, kind, credentials, cancellationToken);
        }
        catch
        {
            _answered.Remove(profileId);
            throw;
        }
    }

    private void ShowPrompt(Profile profile, bool rejected)
    {
        var prompt = new CredentialPrompt(profile.Id, RequestedKind(profile), rejected, profile.LastError);
        CredentialPrompts = [.. CredentialPrompts.Where(existing => existing.ProfileId != profile.Id), prompt];
    }

    private async Task<Credentials?> SavedCredentialsAsync(string profileId, CredentialKind kind)
    {
        try
        {
            return await credentialStore.GetAsync(profileId, kind, _lifetime.Token);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            // Including a refusal by the credential store: the prompt asks instead.
            LogMessages.CredentialsNotRead(log, error, profileId);
            return null;
        }
    }

    private async Task RemoveSavedCredentialsAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken)
    {
        try
        {
            await credentialStore.RemoveAsync(profileId, kind, cancellationToken);
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            LogMessages.CredentialsNotRemoved(log, error, profileId);
        }
    }
}
