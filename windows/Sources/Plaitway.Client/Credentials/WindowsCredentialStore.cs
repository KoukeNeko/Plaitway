using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using Plaitway.Client.Native;
using Plaitway.V1;

namespace Plaitway.Client.Storage;

/// <summary>The Credential Manager refused a read, a write or a delete.</summary>
public sealed class CredentialStoreException(string operation, int win32Error)
    : Exception($"Credential Manager {operation} failed: {new Win32Exception(win32Error).Message}")
{
    /// <summary>What was attempted: read, write or delete.</summary>
    public string Operation { get; } = operation;

    /// <summary>The Win32 error code.</summary>
    public int Win32Error { get; } = win32Error;
}

/// <summary>
/// The user's Windows Credential Manager vault: one generic credential per profile and kind, with the
/// user name and the password in the credential's blob as the macOS client keeps them in a Keychain
/// item. The vault belongs to the signed-in user, is encrypted with that user's logon secret and, with
/// the persistence chosen here (see <see cref="CredentialNative.PersistLocalMachine"/>), stays on this machine.
/// </summary>
public sealed class WindowsCredentialStore : ICredentialStore
{
    /// <summary>The name this app's credentials are filed under, the same as the macOS Keychain service.</summary>
    public const string DefaultService = "io.github.koukeneko.plaitway.credentials";

    private const string OperationRead = "read";
    private const string OperationWrite = "write";
    private const string OperationDelete = "delete";

    private readonly string _service;

    /// <summary>A store that files its credentials under <paramref name="service"/>; tests use their own to leave nothing of the app's behind.</summary>
    public WindowsCredentialStore(string service = DefaultService)
    {
        _service = service;
    }

    /// <inheritdoc />
    public Task<Credentials?> GetAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        Task.Run(() => Read(TargetName(profileId, kind)), cancellationToken);

    /// <inheritdoc />
    public Task<bool> ContainsAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        Task.Run(() => Read(TargetName(profileId, kind)) is not null, cancellationToken);

    /// <inheritdoc />
    public Task SaveAsync(Credentials credentials, string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        Task.Run(() => Write(TargetName(profileId, kind), credentials), cancellationToken);

    /// <inheritdoc />
    public Task RemoveAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        Task.Run(() => Delete(TargetName(profileId, kind)), cancellationToken);

    /// <summary>The name a credential is filed under in the vault.</summary>
    internal string TargetName(string profileId, CredentialKind kind) => $"{_service}/{profileId}:{KindName(kind)}";

    private static string KindName(CredentialKind kind) => kind == CredentialKind.KeyPassphrase ? "key-passphrase" : "user-password";

    private static unsafe Credentials? Read(string target)
    {
        if (!CredentialNative.CredRead(target, CredentialNative.TypeGeneric, 0, out var buffer))
        {
            var error = Marshal.GetLastPInvokeError();
            return error == CredentialNative.ErrorNotFound ? null : throw new CredentialStoreException(OperationRead, error);
        }

        try
        {
            var credential = Marshal.PtrToStructure<CredentialNative.Credential>(buffer);
            var blob = new byte[credential.CredentialBlobSize];
            Marshal.Copy(credential.CredentialBlob, blob, 0, blob.Length);
            // CredRead hands out a copy of the blob that CredFree does not clear.
            CryptographicOperations.ZeroMemory(new Span<byte>((void*)credential.CredentialBlob, blob.Length));
            return Decode(blob);
        }
        finally
        {
            CredentialNative.CredFree(buffer);
        }
    }

    /// <summary>The credentials in <paramref name="blob"/>, which is overwritten with zeros: the JSON in it holds the password in the clear.</summary>
    internal static Credentials Decode(byte[] blob)
    {
        try
        {
            var stored = JsonSerializer.Deserialize<StoredCredentials>(blob)
                ?? throw new InvalidDataException("the saved credential is empty");
            return new Credentials(stored.Username, stored.Password);
        }
        finally
        {
            CryptographicOperations.ZeroMemory(blob);
        }
    }

    private static void Write(string target, Credentials credentials) =>
        WriteBlob(target, JsonSerializer.SerializeToUtf8Bytes(new StoredCredentials(credentials.Username, credentials.Password)));

    /// <summary>Stores <paramref name="blob"/> and then overwrites it with zeros: the JSON in it holds the password in the clear.</summary>
    internal static unsafe void WriteBlob(string target, byte[] blob)
    {
        try
        {
            if (blob.Length > CredentialNative.MaxBlobSize)
            {
                throw new CredentialStoreException(OperationWrite, CredentialNative.ErrorBadLength);
            }

            fixed (char* targetName = target)
            fixed (byte* blobBytes = blob)
            {
                var credential = new CredentialNative.Credential
                {
                    Type = CredentialNative.TypeGeneric,
                    TargetName = (nint)targetName,
                    CredentialBlobSize = (uint)blob.Length,
                    CredentialBlob = (nint)blobBytes,
                    Persist = CredentialNative.PersistLocalMachine,
                };
                if (!CredentialNative.CredWrite(in credential, 0))
                {
                    throw new CredentialStoreException(OperationWrite, Marshal.GetLastPInvokeError());
                }
            }
        }
        finally
        {
            CryptographicOperations.ZeroMemory(blob);
        }
    }

    private static void Delete(string target)
    {
        if (CredentialNative.CredDelete(target, CredentialNative.TypeGeneric, 0))
        {
            return;
        }

        var error = Marshal.GetLastPInvokeError();
        if (error != CredentialNative.ErrorNotFound)
        {
            throw new CredentialStoreException(OperationDelete, error);
        }
    }

    /// <summary>The blob: the same two fields the macOS client encodes.</summary>
    private sealed record StoredCredentials(
        [property: JsonPropertyName("username")] string Username,
        [property: JsonPropertyName("password")] string Password);
}
