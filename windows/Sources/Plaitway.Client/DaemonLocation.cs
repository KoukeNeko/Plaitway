using Plaitway.Client.Transport;

namespace Plaitway.Client;

/// <summary>Where the daemon listens, and how a development daemon is named.</summary>
public static class DaemonLocation
{
    /// <summary>The environment variable that names the pipe of a daemon started with <c>-socket</c>.</summary>
    public const string OverrideVariable = "PLAITWAY_SOCKET";

    /// <summary>
    /// True in a debug build only. The app trusts the daemon behind its pipe and answers its credential
    /// requests from the Credential Manager, so a release build that followed <c>PLAITWAY_SOCKET</c>
    /// would hand every saved password to whatever process the user, or anything running as the user,
    /// put on that pipe. A compile-time switch cannot be turned on from the environment or by copying the
    /// binary. (The Go command line client has nothing to hand over and always follows the variable.)
    /// </summary>
    public const bool OverrideEnabled =
#if DEBUG
        true;
#else
        false;
#endif

    /// <summary>The pipe the service listens on.</summary>
    public static PipePath ProductionPipe => PipePath.Default;

    /// <summary>The pipe the app connects to: the override in a debug build, otherwise the production pipe.</summary>
    /// <exception cref="InvalidPipePathException">The override names something that is not a local named pipe.</exception>
    public static PipePath Pipe(IReadOnlyDictionary<string, string?> environment) =>
        Override(environment) ?? ProductionPipe;

    /// <summary>
    /// The pipe named by <c>PLAITWAY_SOCKET</c>, for running against a daemon started with <c>-socket</c>
    /// (<c>plaitwayd -fake</c>), or null: always in a build without <see cref="OverrideEnabled"/>, and when
    /// the variable is missing or empty. The app does not manage the daemon behind such a pipe.
    /// </summary>
    /// <exception cref="InvalidPipePathException">The variable names something that is not a local named pipe.</exception>
    public static PipePath? Override(IReadOnlyDictionary<string, string?> environment) =>
        OverrideEnabled ? ParseOverride(environment) : null;

    /// <summary>The override as the Go command line client reads it, whatever the build.</summary>
    internal static PipePath? ParseOverride(IReadOnlyDictionary<string, string?> environment) =>
        environment.TryGetValue(OverrideVariable, out var value) && !string.IsNullOrEmpty(value) ? PipePath.Parse(value) : null;

    /// <summary>The environment of this process, as the other members take it.</summary>
    public static IReadOnlyDictionary<string, string?> CurrentEnvironment()
    {
        var variables = new Dictionary<string, string?>(StringComparer.OrdinalIgnoreCase);
        foreach (System.Collections.DictionaryEntry entry in Environment.GetEnvironmentVariables())
        {
            variables[(string)entry.Key] = (string?)entry.Value;
        }

        return variables;
    }
}
