using System.ComponentModel;
using System.Diagnostics;
using System.Runtime.InteropServices;

namespace Plaitway.Client.Tests.Support;

/// <summary>
/// A job object that ends every process assigned to it when the job is closed, which is when this test
/// process ends, however it ends. A daemon that outlived a crashed test run would keep a pipe, a state
/// directory and the inherited output handles that make <c>dotnet test</c> wait for it.
/// </summary>
internal sealed partial class KillOnCloseJob : IDisposable
{
    private const int ExtendedLimitInformationClass = 9;
    private const uint KillOnJobClose = 0x2000;

    private readonly nint _job;

    [StructLayout(LayoutKind.Sequential)]
    private struct BasicLimitInformation
    {
        public long PerProcessUserTimeLimit;
        public long PerJobUserTimeLimit;
        public uint LimitFlags;
        public nuint MinimumWorkingSetSize;
        public nuint MaximumWorkingSetSize;
        public uint ActiveProcessLimit;
        public nuint Affinity;
        public uint PriorityClass;
        public uint SchedulingClass;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct IoCounters
    {
        public ulong ReadOperationCount;
        public ulong WriteOperationCount;
        public ulong OtherOperationCount;
        public ulong ReadTransferCount;
        public ulong WriteTransferCount;
        public ulong OtherTransferCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct ExtendedLimitInformation
    {
        public BasicLimitInformation Basic;
        public IoCounters Io;
        public nuint ProcessMemoryLimit;
        public nuint JobMemoryLimit;
        public nuint PeakProcessMemoryUsed;
        public nuint PeakJobMemoryUsed;
    }

    public KillOnCloseJob()
    {
        _job = CreateJobObject(nint.Zero, nint.Zero);
        if (_job == nint.Zero)
        {
            throw new Win32Exception(Marshal.GetLastPInvokeError());
        }

        var information = new ExtendedLimitInformation { Basic = new BasicLimitInformation { LimitFlags = KillOnJobClose } };
        if (!SetInformationJobObject(_job, ExtendedLimitInformationClass, ref information, (uint)Marshal.SizeOf<ExtendedLimitInformation>()))
        {
            throw new Win32Exception(Marshal.GetLastPInvokeError());
        }
    }

    /// <summary>Puts a running process in the job.</summary>
    public void Add(Process process)
    {
        if (!AssignProcessToJobObject(_job, process.Handle))
        {
            throw new Win32Exception(Marshal.GetLastPInvokeError());
        }
    }

    /// <inheritdoc />
    public void Dispose() => CloseHandle(_job);

    [LibraryImport("kernel32.dll", EntryPoint = "CreateJobObjectW", SetLastError = true)]
    private static partial nint CreateJobObject(nint attributes, nint name);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetInformationJobObject(nint job, int informationClass, ref ExtendedLimitInformation information, uint length);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool AssignProcessToJobObject(nint job, nint process);

    [LibraryImport("kernel32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool CloseHandle(nint handle);
}
