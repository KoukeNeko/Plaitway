using System.Collections.Concurrent;
using Plaitway.V1;

namespace Plaitway.Client.Storage;

/// <summary>For tests and for running without the Credential Manager.</summary>
public sealed class InMemoryCredentialStore : ICredentialStore
{
    private readonly ConcurrentDictionary<(string ProfileId, CredentialKind Kind), Credentials> _items = new();

    /// <inheritdoc />
    public Task<Credentials?> GetAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        Task.FromResult(_items.GetValueOrDefault((profileId, kind)));

    /// <inheritdoc />
    public Task<bool> ContainsAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        Task.FromResult(_items.ContainsKey((profileId, kind)));

    /// <inheritdoc />
    public Task SaveAsync(Credentials credentials, string profileId, CredentialKind kind, CancellationToken cancellationToken = default)
    {
        _items[(profileId, kind)] = credentials;
        return Task.CompletedTask;
    }

    /// <inheritdoc />
    public Task RemoveAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default)
    {
        _items.TryRemove((profileId, kind), out _);
        return Task.CompletedTask;
    }
}
