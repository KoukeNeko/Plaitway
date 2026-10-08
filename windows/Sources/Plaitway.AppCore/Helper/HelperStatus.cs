namespace Plaitway.AppCore.Helper;

/// <summary>Where the Plaitway helper service stands in the Service Control Manager.</summary>
public enum HelperState
{
    /// <summary>The service is not registered.</summary>
    NotInstalled,

    /// <summary>Registered and not running.</summary>
    Stopped,

    /// <summary>Start pending or continue pending.</summary>
    Starting,

    /// <summary>Running.</summary>
    Running,

    /// <summary>Stop pending, pause pending or paused.</summary>
    Stopping,

    /// <summary>The Service Control Manager could not be asked, or answered with something this app does not know.</summary>
    Unknown,
}

/// <summary>What the app knows about the helper without asking the daemon: the service and the file it runs from.</summary>
/// <param name="State">The service.</param>
/// <param name="ExecutablePath">Where <c>plaitwayd.exe</c> is, or null when it is not where the app looks.</param>
public readonly record struct HelperStatus(HelperState State, string? ExecutablePath)
{
    /// <summary>The service can be registered or started: the program exists.</summary>
    public bool HasExecutable => ExecutablePath is not null;
}
