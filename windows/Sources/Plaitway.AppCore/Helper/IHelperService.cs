namespace Plaitway.AppCore.Helper;

/// <summary>Asks the Service Control Manager how the helper service is. It needs no elevation: the service's access list lets every interactive user read its state.</summary>
public interface IHelperService
{
    /// <summary>The state of the service now.</summary>
    HelperState Query();
}

/// <summary>How an elevated command ended.</summary>
public enum ElevatedOutcome
{
    /// <summary>It ran to its end; the exit code says whether it worked.</summary>
    Completed,

    /// <summary>The user said no to the consent prompt, so nothing ran.</summary>
    Declined,
}

/// <summary>The result of <see cref="IElevatedLauncher.RunAsync"/>.</summary>
/// <param name="Outcome">Whether the command ran.</param>
/// <param name="ExitCode">The exit code of the command; zero when it did not run.</param>
public readonly record struct ElevatedResult(ElevatedOutcome Outcome, int ExitCode);

/// <summary>
/// Runs a program elevated, which makes Windows ask the user for consent. The prompt is the user's: nothing
/// else may answer it.
/// </summary>
public interface IElevatedLauncher
{
    /// <summary>Runs <paramref name="executablePath"/> with <paramref name="arguments"/> and waits for it to end.</summary>
    /// <exception cref="System.ComponentModel.Win32Exception">The program could not be started for a reason other than a refusal of the prompt.</exception>
    Task<ElevatedResult> RunAsync(string executablePath, string arguments, CancellationToken cancellationToken = default);
}
