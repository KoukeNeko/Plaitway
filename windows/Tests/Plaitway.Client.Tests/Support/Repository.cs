using System.Text;

namespace Plaitway.Client.Tests.Support;

/// <summary>The checkout the tests run in: profiles the daemon's own tests use, and the daemon's source.</summary>
internal static class Repository
{
    private const string RootMarker = "go.mod";

    private static readonly UTF8Encoding StrictUtf8 = new(encoderShouldEmitUTF8Identifier: false, throwOnInvalidBytes: true);

    /// <summary>The root of the repository, found from where the tests run.</summary>
    public static string Root { get; } = FindRoot();

    /// <summary>A text file of the repository, such as a profile in <c>internal/ovpn/testdata</c>, byte for byte.</summary>
    public static string Text(string relativePath) =>
        StrictUtf8.GetString(File.ReadAllBytes(Path.Combine(Root, relativePath.Replace('/', Path.DirectorySeparatorChar))));

    private static string FindRoot()
    {
        for (var directory = new DirectoryInfo(AppContext.BaseDirectory); directory is not null; directory = directory.Parent)
        {
            if (File.Exists(Path.Combine(directory.FullName, RootMarker)) && Directory.Exists(Path.Combine(directory.FullName, "proto")))
            {
                return directory.FullName;
            }
        }

        throw new InvalidOperationException($"no {RootMarker} above {AppContext.BaseDirectory}");
    }
}
