using Plaitway.Client.Tests.Support;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>The sources of the app, for the tests that check a rule across all of them.</summary>
internal static class SourceFiles
{
    private static readonly string[] SourceProjects = ["Plaitway.App", "Plaitway.AppCore"];

    private static readonly string[] BuildOutputs = [$"{Path.DirectorySeparatorChar}obj{Path.DirectorySeparatorChar}", $"{Path.DirectorySeparatorChar}bin{Path.DirectorySeparatorChar}"];

    /// <summary>The root of the Windows sources in the checkout.</summary>
    public static string WindowsRoot { get; } = Path.Combine(Repository.Root, "windows");

    /// <summary>Every file of these extensions the app is written in, with its text; what the build generates is not.</summary>
    public static IReadOnlyList<(string Name, string Text)> Read(params string[] extensions) =>
    [
        .. SourceProjects
            .Select(project => Path.Combine(WindowsRoot, "Sources", project))
            .Where(Directory.Exists)
            .SelectMany(directory => Directory.EnumerateFiles(directory, "*", SearchOption.AllDirectories))
            .Where(path => extensions.Contains(Path.GetExtension(path), StringComparer.OrdinalIgnoreCase))
            .Where(path => !BuildOutputs.Any(output => path.Contains(output, StringComparison.Ordinal)))
            .Select(path => (Path.GetRelativePath(WindowsRoot, path), File.ReadAllText(path))),
    ];
}
