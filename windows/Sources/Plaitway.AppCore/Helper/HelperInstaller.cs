using CommunityToolkit.Mvvm.ComponentModel;

namespace Plaitway.AppCore.Helper;

/// <summary><c>plaitwayd.exe</c> was not found where the app looks for it, or was found and is not trusted to run elevated.</summary>
/// <param name="distrust">The program that was found and refused; null when there was none.</param>
public sealed class HelperMissingException(HelperDistrust? distrust = null)
    : Exception(distrust is null ? "plaitwayd.exe was not found next to the app" : $"plaitwayd.exe at {distrust.Path} is not trusted: {distrust.Verdict}")
{
    /// <summary>The program that was found and refused; null when there was none.</summary>
    public HelperDistrust? Distrust { get; } = distrust;
};

/// <summary>An elevated helper command ran and failed.</summary>
/// <param name="exitCode">The exit code of <c>plaitwayd.exe</c>; its reason went to a console nobody saw.</param>
public sealed class HelperCommandFailedException(int exitCode) : Exception($"plaitwayd.exe ended with exit code {exitCode}")
{
    /// <summary>The exit code.</summary>
    public int ExitCode { get; } = exitCode;
}

/// <summary>
/// The helper service as the app manages it: what the Service Control Manager says, and the four commands of
/// <c>plaitwayd.exe</c> that change it. Each command is elevated, so each one starts with the consent prompt of
/// Windows, which the user answers. Nothing here runs without being asked.
/// </summary>
public sealed partial class HelperInstaller : ObservableObject
{
    private const string InstallCommand = "install -start";
    private const string StartCommand = "start";
    private const string UpdateCommand = "install -update -start";
    private const string UninstallCommand = "uninstall";

    private readonly IHelperService _service;
    private readonly IElevatedLauncher _launcher;
    private readonly HelperLocator _locator;

    /// <summary>Makes an installer that has looked at the service once.</summary>
    public HelperInstaller(IHelperService service, IElevatedLauncher launcher, HelperLocator locator)
    {
        _service = service;
        _launcher = launcher;
        _locator = locator;
        Status = ReadStatus();
    }

    /// <summary>What the last <see cref="Refresh"/> found.</summary>
    [ObservableProperty]
    public partial HelperStatus Status { get; private set; }

    /// <summary>Looks at the service and for the program again; the user may have approved or started something outside the app.</summary>
    public void Refresh() => Status = ReadStatus();

    /// <summary>Registers the service and starts it. False when the user declined the consent prompt.</summary>
    /// <exception cref="HelperMissingException">There is no program to register.</exception>
    /// <exception cref="HelperCommandFailedException">The command ran and failed.</exception>
    public Task<bool> InstallAsync(CancellationToken cancellationToken = default) => RunAsync(InstallCommand, cancellationToken);

    /// <summary>Starts the service. False when the user declined the consent prompt.</summary>
    /// <exception cref="HelperMissingException">There is no program to ask.</exception>
    /// <exception cref="HelperCommandFailedException">The command ran and failed.</exception>
    public Task<bool> StartAsync(CancellationToken cancellationToken = default) => RunAsync(StartCommand, cancellationToken);

    /// <summary>Replaces the registration of the service with the one of this copy of the program, which restarts it and drops every tunnel. False when the user declined.</summary>
    /// <exception cref="HelperMissingException">There is no program to register.</exception>
    /// <exception cref="HelperCommandFailedException">The command ran and failed.</exception>
    public Task<bool> ReinstallAsync(CancellationToken cancellationToken = default) => RunAsync(UpdateCommand, cancellationToken);

    /// <summary>Stops the service and removes it; the profiles stay on disk. False when the user declined.</summary>
    /// <exception cref="HelperMissingException">There is no program to ask.</exception>
    /// <exception cref="HelperCommandFailedException">The command ran and failed.</exception>
    public Task<bool> UninstallAsync(CancellationToken cancellationToken = default) => RunAsync(UninstallCommand, cancellationToken);

    private HelperStatus ReadStatus()
    {
        var resolution = _locator.Resolve();
        return new HelperStatus(_service.Query(), resolution.TrustedPath, resolution.Distrust);
    }

    private async Task<bool> RunAsync(string arguments, CancellationToken cancellationToken)
    {
        // Judged again here, not taken from the status: the file may have been replaced since the page was drawn.
        var resolution = _locator.Resolve();
        if (resolution.TrustedPath is not { } program)
        {
            // The page that offered this command is out of date.
            Refresh();
            throw new HelperMissingException(resolution.Distrust);
        }

        ElevatedResult result;
        try
        {
            result = await _launcher.RunAsync(program, arguments, cancellationToken);
        }
        finally
        {
            // Whatever happened, the service may be in another state now.
            Refresh();
        }

        if (result.Outcome == ElevatedOutcome.Declined)
        {
            return false;
        }

        return result.ExitCode == 0 ? true : throw new HelperCommandFailedException(result.ExitCode);
    }
}
