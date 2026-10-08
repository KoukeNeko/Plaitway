using System.ComponentModel;
using System.Runtime.InteropServices;

namespace Plaitway.AppCore.Helper;

/// <summary>
/// <see cref="IElevatedLauncher"/> through <c>ShellExecuteEx</c> with the <c>runas</c> verb, which is how a program
/// asks Windows to show the consent prompt. The window of the program is hidden; its exit code is what comes back.
/// </summary>
public sealed partial class ShellElevatedLauncher : IElevatedLauncher
{
    private const string ElevateVerb = "runas";
    private const uint MaskNoCloseProcess = 0x40;
    private const int ShowHidden = 0;
    private const int ErrorCancelled = 1223;
    private const uint WaitTimeoutMilliseconds = 200;
    private const uint WaitObject0 = 0;

    /// <inheritdoc />
    public Task<ElevatedResult> RunAsync(string executablePath, string arguments, CancellationToken cancellationToken = default) =>
        Task.Run(() => Run(executablePath, arguments, cancellationToken), cancellationToken);

    private static ElevatedResult Run(string executablePath, string arguments, CancellationToken cancellationToken)
    {
        var verb = Marshal.StringToCoTaskMemUni(ElevateVerb);
        var file = Marshal.StringToCoTaskMemUni(executablePath);
        var parameters = Marshal.StringToCoTaskMemUni(arguments);
        try
        {
            var info = new ShellExecuteInfo
            {
                Size = (uint)Marshal.SizeOf<ShellExecuteInfo>(),
                Mask = MaskNoCloseProcess,
                Verb = verb,
                File = file,
                Parameters = parameters,
                Show = ShowHidden,
            };
            if (!ShellExecuteEx(ref info))
            {
                var error = Marshal.GetLastPInvokeError();
                return error == ErrorCancelled ? new ElevatedResult(ElevatedOutcome.Declined, 0) : throw new Win32Exception(error);
            }

            return new ElevatedResult(ElevatedOutcome.Completed, WaitForExit(info.Process, cancellationToken));
        }
        finally
        {
            Marshal.FreeCoTaskMem(verb);
            Marshal.FreeCoTaskMem(file);
            Marshal.FreeCoTaskMem(parameters);
        }
    }

    /// <summary>Waits in short slices so that a cancelled caller stops waiting; the elevated program keeps running, as it would after the app is closed.</summary>
    private static int WaitForExit(nint process, CancellationToken cancellationToken)
    {
        try
        {
            while (WaitForSingleObject(process, WaitTimeoutMilliseconds) != WaitObject0)
            {
                cancellationToken.ThrowIfCancellationRequested();
            }

            return GetExitCodeProcess(process, out var exitCode) ? (int)exitCode : throw new Win32Exception(Marshal.GetLastPInvokeError());
        }
        finally
        {
            CloseHandle(process);
        }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ShellExecuteInfo
    {
        public uint Size;
        public uint Mask;
        public nint Window;
        public nint Verb;
        public nint File;
        public nint Parameters;
        public nint Directory;
        public int Show;
        public nint Instance;
        public nint IdList;
        public nint Class;
        public nint ClassKey;
        public uint HotKey;
        public nint Icon;
        public nint Process;
    }

    [LibraryImport("shell32.dll", EntryPoint = "ShellExecuteExW", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool ShellExecuteEx(ref ShellExecuteInfo info);

    [LibraryImport("kernel32.dll")]
    private static partial uint WaitForSingleObject(nint handle, uint milliseconds);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool GetExitCodeProcess(nint process, out uint exitCode);

    [LibraryImport("kernel32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool CloseHandle(nint handle);
}
