using Microsoft.Extensions.Logging;
using Plaitway.AppCore.Navigation;
using Plaitway.AppCore.Presentation;
using Plaitway.Client.Import;
using Plaitway.V1;

namespace Plaitway.AppCore;

// Commands on profiles, on diagnostics and on credentials. Each sends a request to the daemon; the result comes back
// through the store. A failure becomes the alert.
public sealed partial class AppModel
{
    private static readonly TimeSpan SelectionWait = TimeSpan.FromSeconds(2);
    private static readonly TimeSpan SelectionPoll = TimeSpan.FromMilliseconds(20);

    /// <summary>Connects or disconnects a profile; enabling a failed profile retries it.</summary>
    public Task SetEnabledAsync(string profileId, bool enabled, CancellationToken cancellationToken = default) =>
        TryAsync(() => Store.SetEnabledAsync(profileId, enabled, cancellationToken), AlertTitle.ConnectFailed);

    /// <summary>Switches off every profile that is on. Returns whether all of them are off; a profile that stays on is reported.</summary>
    public async Task<bool> DisconnectAllAsync(CancellationToken cancellationToken = default)
    {
        // Together: each stop can take seconds, and the app waits for the last of them to quit.
        var stops = SwitchedOnProfiles.Select(profile => StopAsync(profile.Id, cancellationToken)).ToList();
        var failures = (await Task.WhenAll(stops)).OfType<Exception>().ToList();
        foreach (var failure in failures)
        {
            Report(failure, AlertTitle.ConnectFailed);
        }

        return failures.Count == 0;
    }

    /// <summary>Imports the files in order and selects the last profile that was stored.</summary>
    public Task ImportFilesAsync(IEnumerable<string> paths, CancellationToken cancellationToken = default) =>
        ImportAsync(paths, [], cancellationToken);

    /// <summary>Imports what was dropped on the window; the items that are not files are reported with the failures of the others.</summary>
    public Task ImportDroppedAsync(DropBatch batch, CancellationToken cancellationToken = default) =>
        ImportAsync(batch.Files, batch.Ignored, cancellationToken);

    private async Task ImportAsync(IEnumerable<string> paths, IReadOnlyList<ImportOutcome> ignored, CancellationToken cancellationToken)
    {
        List<ImportOutcome> outcomes = [];
        string? lastImported = null;
        foreach (var path in paths)
        {
            var filename = Path.GetFileName(path);
            try
            {
                var loaded = await ProfileImporter.LoadAsync(path, cancellationToken);
                var result = await Store.ImportProfileAsync(loaded.Content, filename, loaded.Credentials, cancellationToken: cancellationToken);
                lastImported = result.Profile.Id;
                outcomes.Add(new ImportOutcome.Imported(filename, result.Profile.Name, result.Warnings));
            }
            catch (Exception error) when (error is not OperationCanceledException)
            {
                LogMessages.ImportFailed(_log, error, filename);
                outcomes.Add(new ImportOutcome.Failed(filename, Errors.UserMessage(error)));
            }
        }

        outcomes.AddRange(ignored);
        var report = new ImportReport(outcomes);
        if (report.NeedsAttention)
        {
            ImportReport = report;
        }

        if (lastImported is not null)
        {
            await SelectWhenShownAsync(lastImported, cancellationToken);
        }
    }

    /// <summary>Asks for the user's confirmation before a profile is deleted.</summary>
    public void RequestDeletion(string profileId) => PendingDeletion = profileId;

    /// <summary>Deletes a profile, with the credentials saved for it.</summary>
    public async Task DeleteAsync(string profileId, CancellationToken cancellationToken = default) =>
        _ = await TryAsync(() => Store.DeleteProfileAsync(profileId, cancellationToken), AlertTitle.DeleteFailed);

    /// <summary>Moves the profiles at <paramref name="fromOffsets"/> in front of the one at <paramref name="toOffset"/>, as a list drag does.</summary>
    public Task MoveAsync(IReadOnlyCollection<int> fromOffsets, int toOffset) =>
        ApplyOrderAsync(ProfileOrder.Moving([.. Store.Profiles.Select(profile => profile.Id)], fromOffsets, toOffset));

    /// <summary>Moves a profile up (-1) or down (+1) by one place; nothing happens at either end.</summary>
    public async Task MoveAsync(string profileId, int delta)
    {
        var ids = ProfileOrder.Moving([.. Store.Profiles.Select(profile => profile.Id)], profileId, delta);
        if (ids is not null)
        {
            await ApplyOrderAsync(ids);
        }
    }

    /// <summary>Sends the priority order; <paramref name="ids"/> lists every profile, highest priority first.</summary>
    public async Task ApplyOrderAsync(IReadOnlyList<string> ids, CancellationToken cancellationToken = default) =>
        _ = await TryAsync(() => Store.ReorderAsync(ids, cancellationToken), AlertTitle.ReorderFailed);

    /// <summary>
    /// Gives a profile a name. Returns whether the profile has the name, or is about to; when it does not, the text the
    /// user typed is no longer worth showing.
    /// </summary>
    public async Task<bool> RenameAsync(string profileId, string name, CancellationToken cancellationToken = default)
    {
        var trimmed = name.Trim();
        if (Store.Find(profileId) is not { } profile || trimmed.Length == 0)
        {
            return false;
        }

        return trimmed == profile.Name
            || await TryAsync(() => Store.UpdateProfileAsync(profileId, name: trimmed, cancellationToken: cancellationToken), AlertTitle.SaveFailed);
    }

    /// <summary>Changes one setting; the others stay as the daemon reports them.</summary>
    public async Task ChangeSettingsAsync(string profileId, Action<ProfileSettings> change, CancellationToken cancellationToken = default)
    {
        if (Store.Find(profileId)?.Settings is not { } current)
        {
            return;
        }

        var settings = current.Clone();
        change(settings);
        _ = await TryAsync(() => Store.UpdateProfileAsync(profileId, settings: settings, cancellationToken: cancellationToken), AlertTitle.SaveFailed);
    }

    /// <summary>Re-reads the network and rebuilds every route and DNS entry the daemon owns.</summary>
    public async Task ResyncAsync(CancellationToken cancellationToken = default) =>
        _ = await TryAsync(() => Store.ResyncAsync(cancellationToken), AlertTitle.ResyncFailed);

    /// <summary>Removes a route that Diagnostics listed as stale.</summary>
    public async Task RemoveStaleRouteAsync(string key, CancellationToken cancellationToken = default)
    {
        try
        {
            await Store.RemoveStaleRouteAsync(key, cancellationToken);
        }
        catch (Exception error) when (Client.DaemonFailure.From(error).Kind == Client.DaemonFailureKind.NotFound)
        {
            // Already gone: a refresh or a resync removed it while the dialog was open.
            LogMessages.StaleRouteAlreadyGone(_log, key);
        }
        catch (OperationCanceledException)
        {
            // The page was left.
        }
        catch (Exception error)
        {
            Report(error, AlertTitle.RemoveFailed);
        }
    }

    /// <summary>Answers the credential request of a profile with what the user typed.</summary>
    public async Task SubmitCredentialsAsync(string profileId, string username, string password, CancellationToken cancellationToken = default) =>
        _ = await TryAsync(() => Store.ProvideCredentialsAsync(profileId, username, password, cancellationToken), AlertTitle.ConnectFailed);

    /// <summary>The user gave up on the prompt: the profile stops connecting.</summary>
    public async Task CancelCredentialsAsync(string profileId, CancellationToken cancellationToken = default) =>
        _ = await TryAsync(() => Store.CancelCredentialsAsync(profileId, cancellationToken), AlertTitle.ConnectFailed);

    private async Task<Exception?> StopAsync(string profileId, CancellationToken cancellationToken)
    {
        try
        {
            await Store.SetEnabledAsync(profileId, enabled: false, cancellationToken);
            return null;
        }
        catch (Exception error) when (error is not OperationCanceledException)
        {
            return error;
        }
    }

    /// <summary>Selects a profile once the store shows it; the daemon's event may arrive after the reply.</summary>
    private async Task SelectWhenShownAsync(string profileId, CancellationToken cancellationToken)
    {
        var deadline = Time.GetUtcNow() + SelectionWait;
        while (Store.Find(profileId) is null && Time.GetUtcNow() < deadline)
        {
            await Task.Delay(SelectionPoll, Time, cancellationToken);
        }

        if (Store.Find(profileId) is not null)
        {
            Selection = SidebarItem.Profile(profileId);
        }
    }
}
