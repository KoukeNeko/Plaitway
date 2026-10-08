namespace Plaitway.Client.Import;

/// <summary>Why a profile file could not be read for import; the UI words it.</summary>
public abstract record ProfileImportFailure
{
    private protected ProfileImportFailure()
    {
    }

    /// <summary>The profile or a file it refers to cannot be read.</summary>
    /// <param name="Path">The file.</param>
    /// <param name="Reason">What Windows said.</param>
    public sealed record Unreadable(string Path, string Reason) : ProfileImportFailure;

    /// <summary>A file is not text; profiles and the files they refer to are. A directory, a device and a pipe are not text either.</summary>
    /// <param name="Path">The file.</param>
    public sealed record NotText(string Path) : ProfileImportFailure;

    /// <summary>A file is larger than it may be, or inlining makes the profile larger than the daemon takes.</summary>
    /// <param name="Path">The file that went over the limit.</param>
    public sealed record TooLarge(string Path) : ProfileImportFailure;

    /// <summary>A directive names a file that does not exist.</summary>
    /// <param name="Directive">The directive, such as <c>ca</c>.</param>
    /// <param name="Path">Where the file was looked for.</param>
    public sealed record MissingFile(string Directive, string Path) : ProfileImportFailure;

    /// <summary>A directive names a file that holds no PEM data.</summary>
    /// <param name="Directive">The directive, such as <c>key</c>.</param>
    /// <param name="Path">The file.</param>
    public sealed record NotKeyMaterial(string Directive, string Path) : ProfileImportFailure;

    /// <summary>A directive names a file that is not inside the profile's directory.</summary>
    /// <param name="Directive">The directive, such as <c>key</c>.</param>
    /// <param name="Path">The path as the profile wrote it.</param>
    public sealed record OutsideProfileDirectory(string Directive, string Path) : ProfileImportFailure;

    /// <summary>An <c>auth-user-pass</c> file without a user name and a password on its first two lines.</summary>
    /// <param name="Path">The file.</param>
    public sealed record NotCredentials(string Path) : ProfileImportFailure;
}

/// <summary>A profile file could not be read for import.</summary>
/// <param name="failure">Why.</param>
public sealed class ProfileImportException(ProfileImportFailure failure) : Exception(failure.ToString())
{
    /// <summary>Why the import failed.</summary>
    public ProfileImportFailure Failure { get; } = failure;
}
