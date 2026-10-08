using System.Runtime.InteropServices;

namespace Plaitway.Client.Native;

/// <summary>The Windows Credential Manager (wincred.h).</summary>
internal static partial class CredentialNative
{
    /// <summary>CRED_TYPE_GENERIC: an application's own secret, not a login the system uses.</summary>
    public const uint TypeGeneric = 1;

    /// <summary>
    /// CRED_PERSIST_LOCAL_MACHINE: kept across logons in the user's own vault on this machine, protected
    /// with the user's logon secret, and not roamed to other machines with a domain profile. Not
    /// CRED_PERSIST_SESSION, which would ask the user again after every sign-in, and not
    /// CRED_PERSIST_ENTERPRISE, which roams with the account: a VPN password belongs to this machine's
    /// daemon.
    /// </summary>
    public const uint PersistLocalMachine = 2;

    /// <summary>ERROR_NOT_FOUND: there is no such credential.</summary>
    public const int ErrorNotFound = 1168;

    /// <summary>ERROR_BAD_LENGTH: the credential does not fit the blob of the Credential Manager.</summary>
    public const int ErrorBadLength = 24;

    /// <summary>The most bytes a credential blob can hold (CRED_MAX_CREDENTIAL_BLOB_SIZE).</summary>
    public const int MaxBlobSize = 5 * 512;

    [StructLayout(LayoutKind.Sequential)]
    public struct Credential
    {
        public uint Flags;
        public uint Type;
        public nint TargetName;
        public nint Comment;
        public long LastWritten;
        public uint CredentialBlobSize;
        public nint CredentialBlob;
        public uint Persist;
        public uint AttributeCount;
        public nint Attributes;
        public nint TargetAlias;
        public nint UserName;
    }

    [LibraryImport("advapi32.dll", EntryPoint = "CredWriteW", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool CredWrite(in Credential credential, uint flags);

    [LibraryImport("advapi32.dll", EntryPoint = "CredReadW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool CredRead(string targetName, uint type, uint flags, out nint credential);

    [LibraryImport("advapi32.dll", EntryPoint = "CredDeleteW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool CredDelete(string targetName, uint type, uint flags);

    [LibraryImport("advapi32.dll")]
    public static partial void CredFree(nint buffer);
}
