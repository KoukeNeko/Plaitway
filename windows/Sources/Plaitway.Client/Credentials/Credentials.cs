using System.Text;
using Plaitway.V1;

namespace Plaitway.Client.Storage;

/// <summary>What stands in <see cref="object.ToString"/> for a secret, so that a log line or a debugger view never shows one.</summary>
internal static class Redaction
{
    public const string Hidden = "[hidden]";
}

/// <summary>What a profile asked for. <see cref="Username"/> is empty for a key passphrase.</summary>
/// <param name="Username">The user name; empty for a key passphrase.</param>
/// <param name="Password">The password, or the key passphrase.</param>
public sealed record Credentials(string Username, string Password)
{
    /// <summary>A key passphrase: no user name.</summary>
    public Credentials(string password)
        : this(string.Empty, password)
    {
    }

    /// <summary>Hides <see cref="Password"/> from the text the record prints for itself.</summary>
    private bool PrintMembers(StringBuilder builder)
    {
        builder.Append(nameof(Username)).Append(" = ").Append(Username).Append(", ").Append(nameof(Password)).Append(" = ").Append(Redaction.Hidden);
        return true;
    }
}

/// <summary>
/// Where the user's answers to credential requests are kept between connections. The daemon never keeps
/// them on disk, so this is the only copy.
/// </summary>
public interface ICredentialStore
{
    /// <summary>The saved answer, or null.</summary>
    Task<Credentials?> GetAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default);

    /// <summary>Whether there is an answer, without handing it out.</summary>
    Task<bool> ContainsAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default);

    /// <summary>Keeps the answer, replacing an earlier one.</summary>
    Task SaveAsync(Credentials credentials, string profileId, CredentialKind kind, CancellationToken cancellationToken = default);

    /// <summary>Forgets the answer; nothing happens when there is none.</summary>
    Task RemoveAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default);
}

/// <summary>Conveniences over <see cref="ICredentialStore"/>.</summary>
public static class CredentialStoreExtensions
{
    private static readonly CredentialKind[] StoredKinds = [CredentialKind.UserPassword, CredentialKind.KeyPassphrase];

    /// <summary>Forgets every answer saved for the profile, for a profile that is deleted.</summary>
    public static async Task ForgetProfileAsync(this ICredentialStore store, string profileId, CancellationToken cancellationToken = default)
    {
        foreach (var kind in StoredKinds)
        {
            await store.RemoveAsync(profileId, kind, cancellationToken).ConfigureAwait(false);
        }
    }
}
