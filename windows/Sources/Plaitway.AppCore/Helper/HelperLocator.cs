namespace Plaitway.AppCore.Helper;

/// <summary>
/// Finds <c>plaitwayd.exe</c> from where the app runs: the one place that knows the layouts. An installed copy keeps
/// it next to the app, or one folder up when the app has a folder of its own; a development build finds the one that
/// <c>go build -o bin\plaitwayd.exe</c> made in the repository.
/// </summary>
/// <param name="appDirectory">The folder of the app's executable.</param>
/// <param name="fileExists">Whether a file exists; a parameter so that the layouts can be tested without a disk.</param>
public sealed class HelperLocator(string appDirectory, Func<string, bool> fileExists)
{
    /// <summary>The name of the helper's program.</summary>
    public const string ExecutableName = "plaitwayd.exe";

    private const string RepositoryMarker = "go.mod";
    private const string RepositoryBuildFolder = "bin";
    private const int MaxDevelopmentSearchDepth = 8;

    /// <summary>The program, or null when it is in none of the places.</summary>
    public string? Find() => Candidates().FirstOrDefault(fileExists);

    /// <summary>Where the program may be, the most likely first.</summary>
    public IEnumerable<string> Candidates()
    {
        yield return Path.Combine(appDirectory, ExecutableName);

        var parent = Path.GetDirectoryName(appDirectory.TrimEnd(Path.DirectorySeparatorChar));
        if (parent is not null)
        {
            yield return Path.Combine(parent, ExecutableName);
        }

        foreach (var repository in RepositoryRootsAbove(parent))
        {
            yield return Path.Combine(repository, RepositoryBuildFolder, ExecutableName);
        }
    }

    private IEnumerable<string> RepositoryRootsAbove(string? start)
    {
        var current = start;
        for (var depth = 0; current is not null && depth < MaxDevelopmentSearchDepth; depth++)
        {
            if (fileExists(Path.Combine(current, RepositoryMarker)))
            {
                yield return current;
            }

            current = Path.GetDirectoryName(current);
        }
    }
}
