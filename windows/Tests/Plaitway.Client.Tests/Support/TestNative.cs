using System.Runtime.InteropServices;
using System.Security.Principal;
using Microsoft.Win32.SafeHandles;

namespace Plaitway.Client.Tests.Support;

/// <summary>Windows calls only the tests need: what access a handle really has, and a token that is an ordinary user.</summary>
internal static partial class TestNative
{
    private const int ObjectBasicInformationClass = 0;
    private const uint SecurityImpersonation = 2;
    private const uint TokenImpersonation = 2;
    private const uint TokenAllAccess = 0xF01FF;

    [StructLayout(LayoutKind.Sequential)]
    private struct ObjectBasicInformation
    {
        public uint Attributes;
        public uint GrantedAccess;
        public uint HandleCount;
        public uint PointerCount;
        public uint PagedPoolCharge;
        public uint NonPagedPoolCharge;
        public uint Reserved0;
        public uint Reserved1;
        public uint Reserved2;
        public uint NameInfoSize;
        public uint TypeInfoSize;
        public uint SecurityDescriptorSize;
        public long CreationTime;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct SidAndAttributes
    {
        public nint Sid;
        public uint Attributes;
    }

    [LibraryImport("ntdll.dll")]
    private static partial int NtQueryObject(SafeHandle handle, int informationClass, out ObjectBasicInformation information, uint length, out uint returned);

    [LibraryImport("advapi32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static unsafe partial bool CreateRestrictedToken(
        SafeAccessTokenHandle existing, uint flags, uint disableSidCount, SidAndAttributes* sidsToDisable,
        uint deletePrivilegeCount, nint privilegesToDelete, uint restrictSidCount, nint sidsToRestrict, out nint newToken);

    [LibraryImport("advapi32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool DuplicateTokenEx(
        nint existing, uint desiredAccess, nint attributes, uint impersonationLevel, uint tokenType, out nint newToken);

    [LibraryImport("kernel32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool CloseHandle(nint handle);

    /// <summary>The access mask the handle was opened with, as the kernel granted it.</summary>
    public static uint GrantedAccess(SafeHandle handle)
    {
        var status = NtQueryObject(handle, ObjectBasicInformationClass, out var information, (uint)Marshal.SizeOf<ObjectBasicInformation>(), out _);
        return status == 0 ? information.GrantedAccess : throw new InvalidOperationException($"NtQueryObject failed with 0x{status:X8}");
    }

    /// <summary>
    /// Runs <paramref name="action"/> on this thread with the user's own SID reduced to deny-only. An
    /// access check then matches no entry for the user itself and sees what any other interactive user
    /// would: the Interactive group and nothing else (the Go test <c>impersonateOrdinaryInteractiveUser</c>).
    /// The action has to be synchronous: the impersonation belongs to the thread.
    /// </summary>
    public static unsafe void AsOrdinaryInteractiveUser(Action action)
    {
        using var identity = WindowsIdentity.GetCurrent(System.Security.Principal.TokenAccessLevels.Duplicate | System.Security.Principal.TokenAccessLevels.Query);
        var user = identity.User!;
        var userBytes = new byte[user.BinaryLength];
        user.GetBinaryForm(userBytes, 0);

        nint restricted;
        fixed (byte* userPointer = userBytes)
        {
            var disable = new SidAndAttributes { Sid = (nint)userPointer };
            if (!CreateRestrictedToken(identity.AccessToken, 0, 1, &disable, 0, nint.Zero, 0, nint.Zero, out restricted))
            {
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastPInvokeError());
            }
        }

        try
        {
            if (!DuplicateTokenEx(restricted, TokenAllAccess, nint.Zero, SecurityImpersonation, TokenImpersonation, out var impersonation))
            {
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastPInvokeError());
            }

            using var handle = new SafeAccessTokenHandle(impersonation);
            WindowsIdentity.RunImpersonated(handle, action);
        }
        finally
        {
            CloseHandle(restricted);
        }
    }
}
