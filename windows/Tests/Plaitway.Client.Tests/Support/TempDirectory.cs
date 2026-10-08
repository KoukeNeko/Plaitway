namespace Plaitway.Client.Tests.Support;

/// <summary>A scratch directory that is removed, links and all, when the test is done.</summary>
internal sealed class TempDirectory : IDisposable
{
    private const string Prefix = "pw-test-";

    public TempDirectory()
    {
        Path = System.IO.Path.Combine(System.IO.Path.GetTempPath(), Prefix + Guid.NewGuid().ToString("N")[..8]);
        Directory.CreateDirectory(Path);
    }

    public string Path { get; }

    /// <summary>A directory with the given files; a name may hold a sub-directory (<c>keys/client.key</c>).</summary>
    public static TempDirectory With(IReadOnlyDictionary<string, string> files)
    {
        var directory = new TempDirectory();
        foreach (var (name, content) in files)
        {
            directory.Write(name, content);
        }

        return directory;
    }

    public string Resolve(string name) => System.IO.Path.Combine(Path, name.Replace('/', System.IO.Path.DirectorySeparatorChar));

    public void Write(string name, string content) => Write(name, System.Text.Encoding.UTF8.GetBytes(content));

    public void Write(string name, byte[] content)
    {
        var path = Resolve(name);
        Directory.CreateDirectory(System.IO.Path.GetDirectoryName(path)!);
        File.WriteAllBytes(path, content);
    }

    /// <summary>
    /// Makes <paramref name="link"/> point at <paramref name="target"/>, or skips the test when this
    /// session may not create links (Windows asks for Developer Mode or elevation).
    /// </summary>
    public void LinkOrSkip(string link, string target, bool isDirectory)
    {
        try
        {
            if (isDirectory)
            {
                Directory.CreateSymbolicLink(Resolve(link), target);
            }
            else
            {
                File.CreateSymbolicLink(Resolve(link), target);
            }
        }
        catch (Exception error) when (error is UnauthorizedAccessException or IOException)
        {
            Assert.Skip($"this session cannot create symbolic links: {error.Message}");
        }
    }

    public void Dispose()
    {
        try
        {
            RemoveTree(Path);
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            throw new InvalidOperationException($"could not remove the scratch directory {Path}", error);
        }
    }

    /// <summary>Deletes links as links: a directory link is removed, never followed into its target.</summary>
    private static void RemoveTree(string path)
    {
        foreach (var directory in Directory.EnumerateDirectories(path))
        {
            var info = new DirectoryInfo(directory);
            if (info.Attributes.HasFlag(FileAttributes.ReparsePoint))
            {
                info.Delete();
            }
            else
            {
                RemoveTree(directory);
            }
        }

        foreach (var file in Directory.EnumerateFiles(path))
        {
            File.Delete(file);
        }

        Directory.Delete(path);
    }
}
