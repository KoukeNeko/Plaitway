using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;

namespace Plaitway.Client.Native;

/// <summary>The file calls that decide, on the handle that will be read, what a name really is.</summary>
internal static partial class FileNative
{
    public const uint GenericRead = 0x80000000;
    public const uint ShareAll = 0x7;
    public const uint OpenExisting = 3;

    /// <summary>FILE_FLAG_BACKUP_SEMANTICS: lets a directory be opened, so that the profile's folder can be resolved like a file.</summary>
    public const uint BackupSemantics = 0x02000000;

    /// <summary>FILE_TYPE_DISK: a file or a directory, not a device, a console or a pipe.</summary>
    public const uint FileTypeDisk = 1;

    public const uint FileAttributeDirectory = 0x10;

    /// <summary>VOLUME_NAME_DOS: the final path with a drive letter or a UNC name.</summary>
    public const uint VolumeNameDos = 0;

    public const int ErrorFileNotFound = 2;
    public const int ErrorPathNotFound = 3;
    public const int ErrorInsufficientBuffer = 122;

    /// <summary>Packed to 4 bytes, as the Windows header is: its FILETIMEs are two DWORDs, not one aligned 64-bit number.</summary>
    [StructLayout(LayoutKind.Sequential, Pack = 4)]
    public struct ByHandleFileInformation
    {
        public uint FileAttributes;
        public long CreationTime;
        public long LastAccessTime;
        public long LastWriteTime;
        public uint VolumeSerialNumber;
        public uint FileSizeHigh;
        public uint FileSizeLow;
        public uint NumberOfLinks;
        public uint FileIndexHigh;
        public uint FileIndexLow;
    }

    [LibraryImport("kernel32.dll", EntryPoint = "CreateFileW", StringMarshalling = StringMarshalling.Utf16, SetLastError = true)]
    public static partial SafeFileHandle CreateFile(
        string fileName, uint desiredAccess, uint shareMode, nint securityAttributes, uint creationDisposition, uint flagsAndAttributes, nint templateFile);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    public static partial uint GetFileType(SafeFileHandle file);

    [LibraryImport("kernel32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    public static partial bool GetFileInformationByHandle(SafeFileHandle file, out ByHandleFileInformation information);

    [LibraryImport("kernel32.dll", EntryPoint = "GetFinalPathNameByHandleW", SetLastError = true)]
    public static unsafe partial uint GetFinalPathNameByHandle(SafeFileHandle file, char* path, uint length, uint flags);
}
