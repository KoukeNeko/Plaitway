using System.Runtime.InteropServices;

namespace Plaitway.AppCore.Helper;

/// <summary>
/// <see cref="IHelperService"/> over the Service Control Manager. It opens the manager with the right to connect and
/// the service with the right to read its status, which are the two an ordinary user has.
/// </summary>
public sealed partial class ScmHelperService(string serviceName = HelperPaths.ServiceName) : IHelperService
{
    private const uint ManagerConnect = 0x1;
    private const uint ServiceQueryStatus = 0x4;
    private const int ErrorServiceDoesNotExist = 1060;

    private const uint StateStopped = 1;
    private const uint StateStartPending = 2;
    private const uint StateStopPending = 3;
    private const uint StateRunning = 4;
    private const uint StateContinuePending = 5;
    private const uint StatePausePending = 6;
    private const uint StatePaused = 7;

    /// <inheritdoc />
    public HelperState Query()
    {
        var manager = OpenSCManager(null, null, ManagerConnect);
        if (manager == nint.Zero)
        {
            return HelperState.Unknown;
        }

        try
        {
            return QueryService(manager);
        }
        finally
        {
            CloseServiceHandle(manager);
        }
    }

    private HelperState QueryService(nint manager)
    {
        var service = OpenService(manager, serviceName, ServiceQueryStatus);
        if (service == nint.Zero)
        {
            return Marshal.GetLastPInvokeError() == ErrorServiceDoesNotExist ? HelperState.NotInstalled : HelperState.Unknown;
        }

        try
        {
            return QueryServiceStatus(service, out var status) ? StateOf(status.CurrentState) : HelperState.Unknown;
        }
        finally
        {
            CloseServiceHandle(service);
        }
    }

    private static HelperState StateOf(uint serviceState) => serviceState switch
    {
        StateStopped => HelperState.Stopped,
        StateStartPending or StateContinuePending => HelperState.Starting,
        StateRunning => HelperState.Running,
        StateStopPending or StatePausePending or StatePaused => HelperState.Stopping,
        _ => HelperState.Unknown,
    };

    [StructLayout(LayoutKind.Sequential)]
    private struct ServiceStatus
    {
        public uint ServiceType;
        public uint CurrentState;
        public uint ControlsAccepted;
        public uint Win32ExitCode;
        public uint ServiceSpecificExitCode;
        public uint CheckPoint;
        public uint WaitHint;
    }

    [LibraryImport("advapi32.dll", EntryPoint = "OpenSCManagerW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    private static partial nint OpenSCManager(string? machineName, string? databaseName, uint access);

    [LibraryImport("advapi32.dll", EntryPoint = "OpenServiceW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    private static partial nint OpenService(nint manager, string name, uint access);

    [LibraryImport("advapi32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool QueryServiceStatus(nint service, out ServiceStatus status);

    [LibraryImport("advapi32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool CloseServiceHandle(nint handle);
}
