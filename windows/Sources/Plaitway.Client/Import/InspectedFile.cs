using System.ComponentModel;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
using Plaitway.Client.Native;

namespace Plaitway.Client.Import;

/// <summary>
/// A file or directory that is open, with what it is decided on the handle itself. A profile decides
/// which files get read, so the answers to "is it a regular file", "how large" and "where does it really
/// live once links are followed" come from the object that is then read, not from a name that something
/// could repoint in between.
/// </summary>
internal sealed class InspectedFile : IDisposable
{
    private const string ExtendedPrefix = @"\\?\";
    private const string ExtendedUncPrefix = @"\\?\UNC\";
    private const string UncPrefix = @"\\";
    private const int InitialPathBuffer = 512;

    private readonly SafeFileHandle _handle;

    private InspectedFile(SafeFileHandle handle, string finalPath, bool isRegularFile, long size)
    {
        _handle = handle;
        FinalPath = finalPath;
        IsRegularFile = isRegularFile;
        Size = size;
    }

    /// <summary>Where the object lives, links and junctions followed, without the <c>\\?\</c> prefix.</summary>
    public string FinalPath { get; }

    /// <summary>True for a file on a disk; false for a directory, a device such as NUL, or a pipe.</summary>
    public bool IsRegularFile { get; }

    /// <summary>The size in bytes; zero for what is not a regular file.</summary>
    public long Size { get; }

    /// <summary>Opens <paramref name="path"/>, which may name a directory.</summary>
    /// <exception cref="Win32Exception">Windows refused; <see cref="Win32Exception.NativeErrorCode"/> says why.</exception>
    public static InspectedFile Open(string path)
    {
        var handle = FileNative.CreateFile(
            path, FileNative.GenericRead, FileNative.ShareAll, nint.Zero, FileNative.OpenExisting, FileNative.BackupSemantics, nint.Zero);
        if (handle.IsInvalid)
        {
            var error = Marshal.GetLastPInvokeError();
            handle.Dispose();
            throw new Win32Exception(error);
        }

        try
        {
            if (FileNative.GetFileType(handle) != FileNative.FileTypeDisk)
            {
                return new InspectedFile(handle, path, isRegularFile: false, size: 0);
            }

            return Describe(handle);
        }
        catch
        {
            handle.Dispose();
            throw;
        }
    }

    /// <summary>Reads at most <paramref name="limit"/> + 1 bytes from the start, so that the caller can tell a file that is too large.</summary>
    public async Task<byte[]> ReadAsync(int limit, CancellationToken cancellationToken) =>
        await Task.Run(() => ReadAtMost(limit + 1), cancellationToken).ConfigureAwait(false);

    /// <inheritdoc />
    public void Dispose() => _handle.Dispose();

    private static InspectedFile Describe(SafeFileHandle handle)
    {
        if (!FileNative.GetFileInformationByHandle(handle, out var information))
        {
            throw new Win32Exception(Marshal.GetLastPInvokeError());
        }

        var isDirectory = (information.FileAttributes & FileNative.FileAttributeDirectory) != 0;
        var size = ((long)information.FileSizeHigh << 32) | information.FileSizeLow;
        return new InspectedFile(handle, FinalPathOf(handle), isRegularFile: !isDirectory, size: isDirectory ? 0 : size);
    }

    private byte[] ReadAtMost(int count)
    {
        var buffer = new byte[(int)Math.Min(count, Size + 1)];
        var read = 0;
        while (read < buffer.Length)
        {
            var chunk = RandomAccess.Read(_handle, buffer.AsSpan(read), read);
            if (chunk == 0)
            {
                break;
            }

            read += chunk;
        }

        return buffer[..read];
    }

    private static unsafe string FinalPathOf(SafeFileHandle handle)
    {
        var buffer = new char[InitialPathBuffer];
        while (true)
        {
            uint length;
            fixed (char* start = buffer)
            {
                length = FileNative.GetFinalPathNameByHandle(handle, start, (uint)buffer.Length, FileNative.VolumeNameDos);
            }

            if (length == 0)
            {
                throw new Win32Exception(Marshal.GetLastPInvokeError());
            }

            if (length < buffer.Length)
            {
                return WithoutExtendedPrefix(new string(buffer, 0, (int)length));
            }

            buffer = new char[length + 1];
        }
    }

    private static string WithoutExtendedPrefix(string path)
    {
        if (path.StartsWith(ExtendedUncPrefix, StringComparison.Ordinal))
        {
            return UncPrefix + path[ExtendedUncPrefix.Length..];
        }

        return path.StartsWith(ExtendedPrefix, StringComparison.Ordinal) ? path[ExtendedPrefix.Length..] : path;
    }
}
