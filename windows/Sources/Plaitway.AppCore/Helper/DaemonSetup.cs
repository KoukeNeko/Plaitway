using Plaitway.AppCore.Daemon;
using Plaitway.Client;

namespace Plaitway.AppCore.Helper;

/// <summary>Where the helper stands, in the terms of what the user can do about it.</summary>
public enum SetupKind
{
    /// <summary>The daemon answers and can be given commands.</summary>
    Ready,

    /// <summary>Connected, but the helper is from another version of the app.</summary>
    VersionMismatch,

    /// <summary>Nothing has been heard yet, or the helper was just started and may still be coming up.</summary>
    Connecting,

    /// <summary>The service is not registered.</summary>
    NeedsInstall,

    /// <summary>The service is registered and not running.</summary>
    Stopped,

    /// <summary>The service is not registered and the program to register is not where the app looks.</summary>
    HelperMissing,

    /// <summary>The service runs, or may, and the daemon is not reachable; <see cref="DaemonSetup.Cause"/> says why.</summary>
    NotResponding,

    /// <summary>A daemon of the developer's (<c>PLAITWAY_SOCKET</c>) is the one that is not answering: the app does not manage the helper.</summary>
    Development,
}

/// <summary>Everything that decides a <see cref="DaemonSetup"/>.</summary>
/// <param name="Connection">Whether the daemon answers.</param>
/// <param name="Cause">Why it does not, when the watch knows.</param>
/// <param name="Helper">The service and the program.</param>
/// <param name="DaemonVersion">The version the daemon reported.</param>
/// <param name="AppVersion">The version of this app; null when unknown.</param>
/// <param name="IsOverridden">The pipe comes from <c>PLAITWAY_SOCKET</c>.</param>
/// <param name="IsSettling">The helper was just installed, started or retried and may still be coming up.</param>
public readonly record struct SetupInputs(
    DaemonConnection Connection,
    DaemonFailureKind? Cause,
    HelperStatus Helper,
    string? DaemonVersion,
    string? AppVersion,
    bool IsOverridden,
    bool IsSettling);

/// <summary>Where the helper stands, from what the app knows: the connection to the daemon, the service and the versions.</summary>
/// <param name="Kind">The state.</param>
/// <param name="DaemonVersion">For <see cref="SetupKind.VersionMismatch"/>, the daemon's.</param>
/// <param name="AppVersion">For <see cref="SetupKind.VersionMismatch"/>, the app's.</param>
/// <param name="Cause">For <see cref="SetupKind.NotResponding"/>, why the daemon cannot be used.</param>
public readonly record struct DaemonSetup(SetupKind Kind, string? DaemonVersion = null, string? AppVersion = null, DaemonFailureKind? Cause = null)
{
    /// <summary>The daemon answers.</summary>
    public static DaemonSetup Ready { get; } = new(SetupKind.Ready);

    /// <summary>Not heard from yet.</summary>
    public static DaemonSetup Connecting { get; } = new(SetupKind.Connecting);

    /// <summary>The helper is of another version than the app.</summary>
    public static DaemonSetup VersionMismatch(string daemon, string app) => new(SetupKind.VersionMismatch, daemon, app);

    /// <summary>The daemon is not reachable although the service is running, for <paramref name="cause"/>.</summary>
    public static DaemonSetup NotResponding(DaemonFailureKind? cause = DaemonFailureKind.Unavailable) => new(SetupKind.NotResponding, Cause: cause);

    /// <summary>Resolves the state from what is known.</summary>
    public static DaemonSetup From(SetupInputs inputs) => inputs.Connection == DaemonConnection.Connected
        ? FromAnswering(inputs)
        : FromSilent(inputs);

    /// <summary>The window opens at launch to say so: the helper has to be installed or started.</summary>
    public bool OffersInstallation => Kind is SetupKind.NeedsInstall or SetupKind.Stopped;

    /// <summary>The daemon answers and can be given commands.</summary>
    public bool IsUsable => Kind is SetupKind.Ready or SetupKind.VersionMismatch;

    private static DaemonSetup FromAnswering(SetupInputs inputs)
    {
        var mismatch = !inputs.IsOverridden
            && inputs.AppVersion is { } app
            && inputs.DaemonVersion is { } daemon
            && app != daemon;
        return mismatch ? VersionMismatch(inputs.DaemonVersion!, inputs.AppVersion!) : Ready;
    }

    private static DaemonSetup FromSilent(SetupInputs inputs)
    {
        var isConnecting = inputs.Connection == DaemonConnection.Connecting;
        if (inputs.IsOverridden)
        {
            return isConnecting ? Connecting : new DaemonSetup(SetupKind.Development);
        }

        // While connecting, a helper that something else installed or started may be about to answer.
        return inputs.Helper.State switch
        {
            HelperState.NotInstalled when !inputs.Helper.HasExecutable => isConnecting ? Connecting : new DaemonSetup(SetupKind.HelperMissing),
            HelperState.NotInstalled => isConnecting ? Connecting : new DaemonSetup(SetupKind.NeedsInstall),
            HelperState.Stopped => isConnecting || inputs.IsSettling ? Connecting : new DaemonSetup(SetupKind.Stopped),
            HelperState.Starting => Connecting,
            _ => isConnecting || inputs.IsSettling ? Connecting : NotResponding(inputs.Cause),
        };
    }
}
