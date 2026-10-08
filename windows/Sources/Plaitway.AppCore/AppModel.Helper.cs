using Microsoft.Extensions.Logging;
using Plaitway.AppCore.Helper;

namespace Plaitway.AppCore;

// The helper: install, start, reinstall and uninstall go through the installer, which asks Windows for consent each time.
public sealed partial class AppModel
{
    /// <summary>The app can add itself to the startup list.</summary>
    public bool CanSetLaunchAtLogin => _startup.IsAvailable;

    /// <summary>Registers the service and starts it. A user who declines the consent prompt has decided; nothing is reported.</summary>
    public Task InstallHelperAsync(CancellationToken cancellationToken = default) => RunHelperActionAsync(Installer.InstallAsync, cancellationToken);

    /// <summary>Starts the registered service.</summary>
    public Task StartHelperAsync(CancellationToken cancellationToken = default) => RunHelperActionAsync(Installer.StartAsync, cancellationToken);

    /// <summary>
    /// Registers the service again, which drops every tunnel; the views ask first (<see cref="IsConfirmingReinstall"/>).
    /// A helper that someone else started is left alone.
    /// </summary>
    public Task ReinstallHelperAsync(CancellationToken cancellationToken = default) =>
        IsHelperExternal ? Task.CompletedTask : RunHelperActionAsync(Installer.ReinstallAsync, cancellationToken);

    /// <summary>Removes the service; its profiles stay on disk.</summary>
    public async Task UninstallHelperAsync(CancellationToken cancellationToken = default)
    {
        _ = await TryAsync(() => Installer.UninstallAsync(cancellationToken), AlertTitle.HelperFailed);
    }

    /// <summary>Looks at the helper again; the daemon may have come up since.</summary>
    public void Retry()
    {
        Installer.Refresh();
        BeginSettling();
    }

    /// <summary>Does what the setup page's button says.</summary>
    public Task PerformAsync(SetupAction action, CancellationToken cancellationToken = default)
    {
        switch (action)
        {
            case SetupAction.InstallHelper:
                return InstallHelperAsync(cancellationToken);
            case SetupAction.StartHelper:
                return StartHelperAsync(cancellationToken);
            case SetupAction.Retry:
                Retry();
                return Task.CompletedTask;
            default:
                IsConfirmingReinstall = true;
                return Task.CompletedTask;
        }
    }

    /// <summary>Adds the app to the startup list or removes it, and shows what the list says afterwards.</summary>
    public void SetLaunchAtLogin(bool enabled)
    {
        try
        {
            _startup.SetEnabled(enabled);
        }
        catch (InvalidOperationException error)
        {
            Report(error, AlertTitle.LaunchAtLoginFailed);
        }

        LaunchAtLogin = _startup.IsEnabled;
    }

    private async Task RunHelperActionAsync(Func<CancellationToken, Task<bool>> action, CancellationToken cancellationToken)
    {
        var ran = await TryAsync(() => action(cancellationToken), AlertTitle.HelperFailed, Text.FallbackColonRunPlaitwaydExeInstallStartAsAdministrator);
        if (ran)
        {
            BeginSettling();
        }
    }

    /// <summary>The helper may need a moment to answer after it was installed, started or retried; until then the window says Connecting.</summary>
    private void BeginSettling()
    {
        IsSettling = true;
        _settling.Cancel();
        _settling.Dispose();
        _settling = new CancellationTokenSource();
        _ = EndSettlingAsync(_settling.Token);
    }

    private async Task EndSettlingAsync(CancellationToken cancellationToken)
    {
        try
        {
            await Task.Delay(SettleDuration, Time, cancellationToken);
            IsSettling = false;
        }
        catch (OperationCanceledException)
        {
            // Another settling began, or the app is closing.
        }
    }

    /// <summary>Tells the user that something failed: the alert says what was attempted and what went wrong.</summary>
    public void Report(Exception error, AlertTitle title, string? hint = null)
    {
        var titleText = AppAlert.TitleText(title, Text);
        LogMessages.CommandFailed(_log, error, titleText);
        var message = string.Join('\n', new[] { Errors.UserMessage(error), hint }.OfType<string>());
        Alert = new AppAlert(Guid.NewGuid(), titleText, message);
    }

    /// <summary>Runs a command and puts its failure in the alert; true when it worked. A cancelled command says nothing.</summary>
    private async Task<bool> TryAsync(Func<Task<bool>> command, AlertTitle title, string? hint = null)
    {
        try
        {
            return await command();
        }
        catch (OperationCanceledException)
        {
            return false;
        }
        catch (Exception error)
        {
            Report(error, title, hint);
            return false;
        }
    }

    private Task<bool> TryAsync(Func<Task> command, AlertTitle title) => TryAsync(
        async () =>
        {
            await command();
            return true;
        },
        title);
}
