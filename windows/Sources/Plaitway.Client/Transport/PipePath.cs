namespace Plaitway.Client.Transport;

/// <summary>The path of the pipe given is not a local named pipe.</summary>
public sealed class InvalidPipePathException(string path)
    : ArgumentException($"{path} is not a named pipe, use {PipePath.Namespace}<name>", nameof(path))
{
    /// <summary>The path as it was given.</summary>
    public string Path { get; } = path;
}

/// <summary>
/// The path of a local named pipe, <c>\\.\pipe\name</c>. Anything else is refused, because opening a file
/// would fail with an error that says nothing about the path being wrong.
/// </summary>
public readonly record struct PipePath
{
    /// <summary>Where the system keeps local named pipes.</summary>
    public const string Namespace = @"\\.\pipe\";

    private const string DefaultName = "plaitway";

    /// <summary>The whole path, <c>\\.\pipe\name</c>.</summary>
    public string FullPath { get; }

    /// <summary>The pipe's name, without the namespace.</summary>
    public string Name { get; }

    private PipePath(string fullPath, string name)
    {
        FullPath = fullPath;
        Name = name;
    }

    /// <summary>The pipe the daemon listens on when it is not told otherwise (<c>transport.DefaultPath</c> in Go).</summary>
    public static PipePath Default => Parse(Namespace + DefaultName);

    /// <summary>Reads <paramref name="path"/>; the namespace is matched without regard to case.</summary>
    /// <exception cref="InvalidPipePathException">The path is not <c>\\.\pipe\</c> followed by a name.</exception>
    public static PipePath Parse(string path) =>
        TryParse(path, out var parsed) ? parsed : throw new InvalidPipePathException(path);

    /// <summary>Reads <paramref name="path"/>; false when it is not a local named pipe path.</summary>
    public static bool TryParse(string? path, out PipePath parsed)
    {
        var hasNamespace = path is not null
            && path.Length > Namespace.Length
            && path.StartsWith(Namespace, StringComparison.OrdinalIgnoreCase);
        parsed = hasNamespace ? new PipePath(path!, path![Namespace.Length..]) : default;
        return hasNamespace;
    }

    /// <inheritdoc />
    public override string ToString() => FullPath;
}
