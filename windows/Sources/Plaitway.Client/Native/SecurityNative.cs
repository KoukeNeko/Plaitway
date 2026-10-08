using System.Runtime.InteropServices;

namespace Plaitway.Client.Native;

/// <summary>The few security calls that .NET does not wrap in the form the pipe check needs.</summary>
internal static partial class SecurityNative
{
    /// <summary>SE_KERNEL_OBJECT: the handle names a kernel object, such as a pipe instance.</summary>
    public const uint KernelObject = 6;

    /// <summary>OWNER_SECURITY_INFORMATION: ask for the owner only.</summary>
    public const uint OwnerSecurityInformation = 1;

    [LibraryImport("advapi32.dll")]
    public static partial uint GetSecurityInfo(
        SafeHandle handle, uint objectType, uint securityInfo, out nint owner, nint group, nint dacl, nint sacl, out nint securityDescriptor);

    [LibraryImport("kernel32.dll")]
    public static partial nint LocalFree(nint memory);
}
