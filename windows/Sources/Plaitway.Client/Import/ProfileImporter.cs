using System.ComponentModel;
using System.Globalization;
using System.Text;
using Plaitway.Client.Config;
using Plaitway.Client.Native;
using Plaitway.Client.Storage;

namespace Plaitway.Client.Import;

/// <summary>A profile ready for <see cref="DaemonClient.ImportProfileAsync"/>.</summary>
/// <param name="Content">The profile as the daemon takes it.</param>
/// <param name="Credentials">What the profile's <c>auth-user-pass</c> file held, if it named one.</param>
public sealed record LoadedProfile(byte[] Content, Credentials? Credentials);

/// <summary>An OpenVPN profile with its referenced files inline.</summary>
/// <param name="Text">The profile, with LF line breaks.</param>
/// <param name="Credentials">What the profile's <c>auth-user-pass</c> file held, if it named one.</param>
public sealed record InlinedProfile(string Text, Credentials? Credentials)
{
    /// <summary>Hides <see cref="Text"/>, which holds the profile's private keys, from the text the record prints for itself.</summary>
    private bool PrintMembers(StringBuilder builder)
    {
        builder.Append(nameof(Text)).Append(" = ").Append(Redaction.Hidden).Append(", ").Append(nameof(Credentials)).Append(" = ").Append(Credentials);
        return true;
    }
}

/// <summary>
/// Reads a profile file for <see cref="DaemonClient.ImportProfileAsync"/>. The daemon accepts content
/// only, never a path, so an OpenVPN profile that refers to its certificates and keys by file name gets
/// those files read here, with the user's own permissions, and put inline as <c>&lt;ca&gt;</c>,
/// <c>&lt;cert&gt;</c>, <c>&lt;key&gt;</c> and the like. A profile is untrusted, so it may only name files
/// inside its own directory; <c>auth-user-pass &lt;file&gt;</c> becomes a bare <c>auth-user-pass</c> and the
/// file's user name and password go to the credential store.
/// </summary>
/// <remarks>
/// The rules are those of <c>importfile.go</c> of the command line client and of the macOS importer.
/// On Windows a file is judged on the handle that is read: links, junctions and short names are followed
/// and the result must lie in the profile's directory, compared without regard to case.
/// </remarks>
public static class ProfileImporter
{
    /// <summary>The daemon refuses larger profiles (<c>profile.MaxContentSize</c>); a hopeless upload need not go over the pipe.</summary>
    public const int MaxProfileSize = 1 << 20;

    /// <summary>Generous for a certificate chain or a key, small enough to rule out reading something that is not one.</summary>
    internal const int MaxReferencedFileSize = 256 << 10;

    private const string AuthUserPass = "auth-user-pass";
    private const string KeyDirection = "key-direction";
    private const string TlsAuth = "tls-auth";
    private const string DiffieHellman = "dh";
    private const string InlineMarker = "[inline]";
    private const string PemMarker = "-----BEGIN";
    private const string WireGuardInterfaceSection = "[Interface]";
    private const string OpenVpnExtension = ".ovpn";
    private const string LineFeed = "\n";

    /// <summary>Directives that name a file and have an inline <c>&lt;tag&gt;</c> form.</summary>
    private static readonly HashSet<string> InlineDirectives =
    [
        "ca", "cert", "key", "dh", "crl-verify", "extra-certs", "tls-auth", "tls-crypt", "tls-crypt-v2",
    ];

    private static readonly UTF8Encoding StrictUtf8 = new(encoderShouldEmitUTF8Identifier: false, throwOnInvalidBytes: true);

    /// <summary>The profile as it is sent to the daemon, with the credentials that came with it.</summary>
    /// <exception cref="ProfileImportException">The file, or one it refers to, cannot be used.</exception>
    public static async Task<LoadedProfile> LoadAsync(string path, CancellationToken cancellationToken = default)
    {
        var data = await ReadProfileAsync(path, cancellationToken).ConfigureAwait(false);
        var text = Encoding.UTF8.GetString(data);
        // A wg-quick file has no file references; its keys are inline already.
        if (!IsOpenVpn(text, Path.GetFileName(path)))
        {
            return new LoadedProfile(data, null);
        }

        var directory = Path.GetDirectoryName(Path.GetFullPath(path)) ?? throw new ProfileImportException(new ProfileImportFailure.Unreadable(path, "no directory"));
        var inlined = await InlineReferencedFilesAsync(text, directory, cancellationToken).ConfigureAwait(false);
        var result = Encoding.UTF8.GetBytes(inlined.Text);
        return result.Length <= MaxProfileSize
            ? new LoadedProfile(result, inlined.Credentials)
            : throw new ProfileImportException(new ProfileImportFailure.TooLarge(path));
    }

    /// <summary>
    /// Replaces <c>ca file</c>, <c>tls-auth file 1</c> and the like by their inline blocks, and
    /// <c>auth-user-pass file</c> by <c>auth-user-pass</c>, returning the file's credentials. Paths are
    /// relative to the profile's directory, as for openvpn, and must stay inside it. Blocks that are
    /// already inline are left alone.
    /// </summary>
    /// <exception cref="ProfileImportException">A referenced file cannot be used.</exception>
    public static Task<InlinedProfile> InlineReferencedFilesAsync(string text, string directory, CancellationToken cancellationToken = default) =>
        new Inliner(directory, cancellationToken).RunAsync(text);

    private static bool IsOpenVpn(string text, string filename)
    {
        if (text.Contains(WireGuardInterfaceSection, StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }

        return filename.EndsWith(OpenVpnExtension, StringComparison.OrdinalIgnoreCase)
            || SplitLines(text).Where(line => line.Length > 0).Any(line => Words(line).FirstOrDefault() is { } word && ConfigText.ToLowerAsDaemon(word) is "remote" or "client");
    }

    // Reading

    private static async Task<byte[]> ReadProfileAsync(string path, CancellationToken cancellationToken)
    {
        using var file = OpenOrFail(path);
        return await ReadTextAsync(file, path, MaxProfileSize, cancellationToken).ConfigureAwait(false);
    }

    private static InspectedFile OpenOrFail(string path)
    {
        try
        {
            return InspectedFile.Open(path);
        }
        catch (Win32Exception error)
        {
            throw new ProfileImportException(new ProfileImportFailure.Unreadable(path, error.Message));
        }
    }

    /// <summary>
    /// A regular text file of at most <paramref name="maxSize"/> bytes. A profile decides which files get
    /// read: a device such as NUL, or a directory, is refused before it is read.
    /// </summary>
    private static async Task<byte[]> ReadTextAsync(InspectedFile file, string path, int maxSize, CancellationToken cancellationToken)
    {
        if (!file.IsRegularFile)
        {
            throw new ProfileImportException(new ProfileImportFailure.NotText(path));
        }

        if (file.Size > maxSize)
        {
            throw new ProfileImportException(new ProfileImportFailure.TooLarge(path));
        }

        byte[] data;
        try
        {
            data = await file.ReadAsync(maxSize, cancellationToken).ConfigureAwait(false);
        }
        catch (IOException error)
        {
            throw new ProfileImportException(new ProfileImportFailure.Unreadable(path, error.Message));
        }

        if (data.Length > maxSize)
        {
            throw new ProfileImportException(new ProfileImportFailure.TooLarge(path));
        }

        return IsText(data) ? data : throw new ProfileImportException(new ProfileImportFailure.NotText(path));
    }

    private static bool IsText(byte[] data)
    {
        if (Array.IndexOf(data, (byte)0) >= 0)
        {
            return false;
        }

        try
        {
            _ = StrictUtf8.GetString(data);
            return true;
        }
        catch (DecoderFallbackException)
        {
            return false;
        }
    }

    // Parsing

    /// <summary>
    /// Splits a text into lines like Swift's <c>Character.isNewline</c>: LF, CR LF, CR, VT, FF, NEL, LS
    /// and PS end a line. The text after the last line break is a line even when it is empty.
    /// </summary>
    private static List<string> SplitLines(string text)
    {
        var lines = new List<string>();
        var start = 0;
        for (var index = 0; index < text.Length; index++)
        {
            if (!IsLineBreak(text[index]))
            {
                continue;
            }

            lines.Add(text[start..index]);
            if (text[index] == '\r' && index + 1 < text.Length && text[index + 1] == '\n')
            {
                index++;
            }

            start = index + 1;
        }

        lines.Add(text[start..]);
        return lines;
    }

    private static bool IsLineBreak(char character) => character is '\n' or '\v' or '\f' or '\r' or '\u0085' or '\u2028' or '\u2029';

    /// <summary>Trims space separators and tabs, which is Foundation's <c>.whitespaces</c>.</summary>
    private static string TrimHorizontal(string line)
    {
        var start = 0;
        var end = line.Length;
        while (start < end && IsHorizontalSpace(line[start]))
        {
            start++;
        }

        while (end > start && IsHorizontalSpace(line[end - 1]))
        {
            end--;
        }

        return line[start..end];
    }

    private static bool IsHorizontalSpace(char character) =>
        character == '\t' || CharUnicodeInfo.GetUnicodeCategory(character) == UnicodeCategory.SpaceSeparator;

    /// <summary>
    /// Splits a configuration line into words like openvpn: double or single quotes group, a backslash
    /// escapes, and a line that starts with <c>#</c> or <c>;</c> is a comment.
    /// </summary>
    private static List<string> Words(string line)
    {
        var trimmed = TrimHorizontal(line);
        var words = new List<string>();
        if (trimmed.Length == 0 || trimmed[0] is '#' or ';')
        {
            return words;
        }

        var current = new StringBuilder();
        var inWord = false;
        char? quote = null;
        var escaped = false;
        foreach (var character in trimmed)
        {
            if (escaped)
            {
                current.Append(character);
                escaped = false;
            }
            else if (character == '\\' && quote != '\'')
            {
                escaped = true;
                inWord = true;
            }
            else if (quote is { } open)
            {
                if (character == open)
                {
                    quote = null;
                }
                else
                {
                    current.Append(character);
                }
            }
            else if (character is '"' or '\'')
            {
                quote = character;
                inWord = true;
            }
            else if (char.IsWhiteSpace(character))
            {
                if (inWord)
                {
                    words.Add(current.ToString());
                }

                current.Clear();
                inWord = false;
            }
            else
            {
                current.Append(character);
                inWord = true;
            }
        }

        if (inWord)
        {
            words.Add(current.ToString());
        }

        return words;
    }

    /// <summary>The tag a line opens: <c>ca</c> for <c>&lt;ca&gt;</c>. Closing tags and tags with arguments open nothing.</summary>
    private static string? OpeningBlockTag(string line)
    {
        if (!line.StartsWith('<') || !line.EndsWith('>') || line.StartsWith("</", StringComparison.Ordinal))
        {
            return null;
        }

        var tag = ConfigText.ToLowerAsDaemon(line[1..^1]);
        return tag.Length == 0 || tag.Contains(' ') ? null : tag;
    }

    private sealed record FileReference(string Directive, string Path, string? KeyDirection);

    private static FileReference? FileReferenceIn(string line)
    {
        var words = Words(line);
        if (words.Count < 2 || !InlineDirectives.Contains(ConfigText.ToLowerAsDaemon(words[0])))
        {
            return null;
        }

        var directive = ConfigText.ToLowerAsDaemon(words[0]);
        // "dh none" switches Diffie-Hellman off; "[inline]" says the data follows.
        if ((directive == DiffieHellman && words[1] == "none") || words[1] == InlineMarker)
        {
            return null;
        }

        return new FileReference(directive, words[1], directive == TlsAuth && words.Count >= 3 ? words[2] : null);
    }

    /// <summary>The file of an <c>auth-user-pass file</c> line.</summary>
    private static string? CredentialsFileIn(string line)
    {
        var words = Words(line);
        return words.Count >= 2 && words[0].Equals(AuthUserPass, StringComparison.OrdinalIgnoreCase) ? words[1] : null;
    }

    /// <summary>Holds what one pass over a profile needs: where it is, how large the result has grown, and what it has produced.</summary>
    private sealed class Inliner(string directory, CancellationToken cancellationToken)
    {
        private readonly List<string> _output = [];
        private int _size;
        private string? _rootPath;

        public async Task<InlinedProfile> RunAsync(string text)
        {
            var lines = SplitLines(text);
            var hasKeyDirection = lines.Any(line => Words(line).FirstOrDefault()?.Equals(KeyDirection, StringComparison.OrdinalIgnoreCase) == true);
            Credentials? credentials = null;
            string? openBlock = null;
            foreach (var line in lines)
            {
                var trimmed = TrimHorizontal(line);
                if (openBlock is not null)
                {
                    if (ConfigText.EqualsLowercased(trimmed, $"</{openBlock}>"))
                    {
                        openBlock = null;
                    }

                    Emit(line, directory);
                    continue;
                }

                if (OpeningBlockTag(trimmed) is { } tag)
                {
                    openBlock = tag;
                    Emit(line, directory);
                }
                else if (CredentialsFileIn(trimmed) is { } credentialsFile)
                {
                    credentials = await ReadCredentialsAsync(credentialsFile).ConfigureAwait(false);
                    Emit(AuthUserPass, directory);
                }
                else if (FileReferenceIn(trimmed) is { } reference)
                {
                    hasKeyDirection = await InlineReferenceAsync(reference, hasKeyDirection).ConfigureAwait(false);
                }
                else
                {
                    Emit(line, directory);
                }
            }

            return new InlinedProfile(string.Join(LineFeed, _output), credentials);
        }

        private async Task<bool> InlineReferenceAsync(FileReference reference, bool hasKeyDirection)
        {
            var content = await ReadKeyMaterialAsync(reference).ConfigureAwait(false);
            Emit($"<{reference.Directive}>", reference.Path);
            Emit(content, reference.Path);
            Emit($"</{reference.Directive}>", reference.Path);
            if (reference.KeyDirection is null || hasKeyDirection)
            {
                return hasKeyDirection;
            }

            Emit($"{KeyDirection} {reference.KeyDirection}", directory);
            return true;
        }

        /// <summary>
        /// The profile is untrusted and may name the same 256 KiB file on every line of 1 MiB: the size of
        /// the result is counted as it grows, not when it is done.
        /// </summary>
        private void Emit(string text, string path)
        {
            _size += Encoding.UTF8.GetByteCount(text) + 1;
            if (_size > MaxProfileSize)
            {
                throw new ProfileImportException(new ProfileImportFailure.TooLarge(path));
            }

            _output.Add(text);
        }

        private async Task<string> ReadKeyMaterialAsync(FileReference reference)
        {
            using var file = ResolveInside(reference.Path, reference.Directive);
            var data = await ReadTextAsync(file, file.FinalPath, MaxReferencedFileSize, cancellationToken).ConfigureAwait(false);
            var text = Encoding.UTF8.GetString(data);
            return text.Contains(PemMarker, StringComparison.Ordinal)
                ? text.Replace("\r\n", LineFeed, StringComparison.Ordinal).Trim()
                : throw new ProfileImportException(new ProfileImportFailure.NotKeyMaterial(reference.Directive, file.FinalPath));
        }

        /// <summary>Like openvpn, the user name is the first line and the password the second.</summary>
        private async Task<Credentials> ReadCredentialsAsync(string path)
        {
            using var file = ResolveInside(path, AuthUserPass);
            var data = await ReadTextAsync(file, file.FinalPath, MaxReferencedFileSize, cancellationToken).ConfigureAwait(false);
            var lines = SplitLines(Encoding.UTF8.GetString(data));
            if (lines.Count < 2 || lines[0].Length == 0 || lines[1].Length == 0)
            {
                throw new ProfileImportException(new ProfileImportFailure.NotCredentials(file.FinalPath));
            }

            return new Credentials(lines[0], lines[1]);
        }

        /// <summary>
        /// The file <paramref name="path"/> names, links followed, when it is inside the profile's directory.
        /// openvpn takes paths relative to the profile, and nothing else is read here: the content goes to
        /// the service, so a profile must not be able to pick up any other file of the user's (<c>key ~\.ssh\id_ed25519</c>).
        /// A path with a drive letter is read only if it leads into the profile's directory too; anything
        /// else that starts at a place of its own (the root of the current drive, a drive's working
        /// directory, a network share, a device) is refused without being opened, because opening a network
        /// path from a hostile profile would send the user's credentials to its server.
        /// </summary>
        private InspectedFile ResolveInside(string path, string directive)
        {
            var outside = new ProfileImportException(new ProfileImportFailure.OutsideProfileDirectory(directive, path));
            var root = RootPath();
            var named = NamedPath(path, root) ?? throw outside;
            var file = OpenNamed(named, directive);
            var rootPrefix = root.EndsWith('\\') ? root : root + '\\';
            if (file.FinalPath.StartsWith(rootPrefix, StringComparison.OrdinalIgnoreCase))
            {
                return file;
            }

            file.Dispose();
            throw outside;
        }

        /// <summary>Where the profile's word <paramref name="path"/> points by name, or null when it leaves the profile's directory before anything is opened.</summary>
        private static string? NamedPath(string path, string root)
        {
            if (path.StartsWith('~'))
            {
                return null;
            }

            if (HasDriveLetter(path))
            {
                return path;
            }

            if (Path.IsPathRooted(path))
            {
                return null;
            }

            // ".." is resolved by name first, so that a file that does not exist is not reported as
            // missing when the profile asked for it outside.
            var components = new List<string>();
            foreach (var part in path.Split(['/', '\\'], StringSplitOptions.RemoveEmptyEntries))
            {
                if (part == "..")
                {
                    if (components.Count == 0)
                    {
                        return null;
                    }

                    components.RemoveAt(components.Count - 1);
                }
                else if (part != ".")
                {
                    components.Add(part);
                }
            }

            return string.Join('\\', [root.TrimEnd('\\'), .. components]);
        }

        /// <summary><c>C:\x</c> or <c>C:/x</c>: a path that names its drive and starts at that drive's root.</summary>
        private static bool HasDriveLetter(string path) =>
            path.Length >= 3 && char.IsAsciiLetter(path[0]) && path[1] == ':' && path[2] is '\\' or '/';
        private InspectedFile OpenNamed(string named, string directive)
        {
            cancellationToken.ThrowIfCancellationRequested();
            try
            {
                return InspectedFile.Open(named);
            }
            catch (Win32Exception error) when (error.NativeErrorCode is FileNative.ErrorFileNotFound or FileNative.ErrorPathNotFound)
            {
                throw new ProfileImportException(new ProfileImportFailure.MissingFile(directive, named));
            }
            catch (Win32Exception error)
            {
                throw new ProfileImportException(new ProfileImportFailure.Unreadable(named, error.Message));
            }
        }

        /// <summary>The profile's directory with links followed, found once.</summary>
        private string RootPath()
        {
            if (_rootPath is not null)
            {
                return _rootPath;
            }

            try
            {
                using var root = InspectedFile.Open(directory);
                _rootPath = root.FinalPath;
                return _rootPath;
            }
            catch (Win32Exception error)
            {
                throw new ProfileImportException(new ProfileImportFailure.Unreadable(directory, error.Message));
            }
        }
    }
}
