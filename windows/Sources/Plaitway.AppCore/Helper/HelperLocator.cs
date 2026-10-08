namespace Plaitway.AppCore.Helper;

/// <summary>Where a <c>plaitwayd.exe</c> may be.</summary>
/// <param name="Path">The file.</param>
/// <param name="IsDevelopmentBuild">It is the one <c>go build</c> made in the repository: the developer's own, in a folder the developer owns.</param>
public readonly record struct HelperCandidate(string Path, bool IsDevelopmentBuild);

/// <summary>What <see cref="HelperLocator.Resolve"/> found.</summary>
/// <param name="TrustedPath">The program that may be run elevated; null when there is none.</param>
/// <param name="Distrust">The program that was found and refused, and why; null when it is missing altogether or was accepted.</param>
public readonly record struct HelperResolution(string? TrustedPath, HelperDistrust? Distrust);

/// <summary>
/// Finds <c>plaitwayd.exe</c> from where the app runs: the one place that knows the layouts. An installed copy keeps
/// it next to the app, or one folder up when the app has a folder of its own; a development build finds the one that
/// <c>go build -o bin\plaitwayd.exe</c> made in the repository. What it finds is run with administrator rights, so
/// it is only returned when <see cref="IHelperTrust"/> accepts it (see <see cref="HelperTrustPolicy"/>).
/// </summary>
/// <param name="appDirectory">The folder of the app's executable.</param>
/// <param name="fileExists">Whether a file exists; a parameter so that the layouts can be tested without a disk.</param>
/// <param name="trust">What Windows says about a file; without one, nothing outside a development build is trusted.</param>
/// <param name="searchesRepository">Whether the repository's build folder is searched: only in a Debug build.</param>
public sealed class HelperLocator(string appDirectory, Func<string, bool> fileExists, IHelperTrust? trust = null, bool searchesRepository = HelperLocator.IsDevelopmentBuild)
{
    /// <summary>The name of the helper's program.</summary>
    public const string ExecutableName = "plaitwayd.exe";

    /// <summary>Whether this build is a Debug build, which is the only one that looks for the helper in the repository it was built from.</summary>
#if DEBUG
    public const bool IsDevelopmentBuild = true;
#else
    public const bool IsDevelopmentBuild = false;
#endif

    private const string RepositoryMarker = "go.mod";
    private const string RepositoryBuildFolder = "bin";
    private const int MaxDevelopmentSearchDepth = 8;

    private readonly IHelperTrust _trust = trust ?? UncheckedHelperTrust.Instance;

    /// <summary>The first program that exists, with the verdict on it. Asked again at every use: the file may have changed.</summary>
    public HelperResolution Resolve()
    {
        var existing = Candidates().Where(candidate => fileExists(candidate.Path)).Cast<HelperCandidate?>().FirstOrDefault();
        if (existing is not { } found)
        {
            return new HelperResolution(null, null);
        }

        // The developer's own build lies in a folder the developer can write to, by nature.
        var verdict = found.IsDevelopmentBuild ? HelperTrustVerdict.Trusted : HelperTrustPolicy.Judge(found.Path, _trust);
        return verdict == HelperTrustVerdict.Trusted
            ? new HelperResolution(found.Path, null)
            : new HelperResolution(null, new HelperDistrust(verdict, found.Path));
    }

    /// <summary>Where the program may be, the most likely first.</summary>
    public IEnumerable<HelperCandidate> Candidates()
    {
        yield return new HelperCandidate(Path.Combine(appDirectory, ExecutableName), IsDevelopmentBuild: false);

        var parent = Path.GetDirectoryName(appDirectory.TrimEnd(Path.DirectorySeparatorChar));
        if (parent is not null)
        {
            yield return new HelperCandidate(Path.Combine(parent, ExecutableName), IsDevelopmentBuild: false);
        }

        if (!searchesRepository)
        {
            yield break;
        }

        foreach (var repository in RepositoryRootsAbove(parent))
        {
            yield return new HelperCandidate(Path.Combine(repository, RepositoryBuildFolder, ExecutableName), IsDevelopmentBuild: true);
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
