using Plaitway.Client.Native;
using Plaitway.Client.Storage;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Storage;

/// <summary>The contract of <see cref="ICredentialStore"/>, run against the in-memory store and the Windows Credential Manager.</summary>
public abstract class CredentialStoreContract
{
    private const string ProfileId = "profile-one";

    protected abstract ICredentialStore CreateStore();

    [Fact]
    public async Task SavesAndReadsAnAnswerPerProfileAndKind()
    {
        var store = CreateStore();
        var typed = new Credentials("alice", "s3cret");

        await store.SaveAsync(typed, ProfileId, CredentialKind.UserPassword);

        Assert.Equal(typed, await store.GetAsync(ProfileId, CredentialKind.UserPassword));
        Assert.True(await store.ContainsAsync(ProfileId, CredentialKind.UserPassword));
        Assert.Null(await store.GetAsync(ProfileId, CredentialKind.KeyPassphrase));
        Assert.False(await store.ContainsAsync(ProfileId, CredentialKind.KeyPassphrase));
        Assert.Null(await store.GetAsync("another-profile", CredentialKind.UserPassword));
        await store.RemoveAsync(ProfileId, CredentialKind.UserPassword);
    }

    [Fact]
    public async Task ASecondSaveReplacesTheFirst()
    {
        var store = CreateStore();
        await store.SaveAsync(new Credentials("alice", "old"), ProfileId, CredentialKind.UserPassword);

        await store.SaveAsync(new Credentials("alice", "new"), ProfileId, CredentialKind.UserPassword);

        Assert.Equal("new", (await store.GetAsync(ProfileId, CredentialKind.UserPassword))!.Password);
        await store.RemoveAsync(ProfileId, CredentialKind.UserPassword);
    }

    [Fact]
    public async Task AKeyPassphraseHasNoUserName()
    {
        var store = CreateStore();

        await store.SaveAsync(new Credentials("phrase"), ProfileId, CredentialKind.KeyPassphrase);

        var read = await store.GetAsync(ProfileId, CredentialKind.KeyPassphrase);
        Assert.Equal(new Credentials(string.Empty, "phrase"), read);
        await store.RemoveAsync(ProfileId, CredentialKind.KeyPassphrase);
    }

    [Fact]
    public async Task RemovingForgetsAndRemovingWhatIsNotThereIsNotAnError()
    {
        var store = CreateStore();
        await store.SaveAsync(new Credentials("alice", "s3cret"), ProfileId, CredentialKind.UserPassword);

        await store.RemoveAsync(ProfileId, CredentialKind.UserPassword);
        await store.RemoveAsync(ProfileId, CredentialKind.UserPassword);

        Assert.False(await store.ContainsAsync(ProfileId, CredentialKind.UserPassword));
    }

    [Fact]
    public async Task ForgettingAProfileRemovesBothKinds()
    {
        var store = CreateStore();
        await store.SaveAsync(new Credentials("alice", "s3cret"), ProfileId, CredentialKind.UserPassword);
        await store.SaveAsync(new Credentials("phrase"), ProfileId, CredentialKind.KeyPassphrase);
        await store.SaveAsync(new Credentials("bob", "pw"), "other", CredentialKind.UserPassword);

        await store.ForgetProfileAsync(ProfileId);

        Assert.False(await store.ContainsAsync(ProfileId, CredentialKind.UserPassword));
        Assert.False(await store.ContainsAsync(ProfileId, CredentialKind.KeyPassphrase));
        Assert.True(await store.ContainsAsync("other", CredentialKind.UserPassword));
        await store.RemoveAsync("other", CredentialKind.UserPassword);
    }

    [Fact]
    public async Task KeepsWhatIsNotAscii()
    {
        var store = CreateStore();
        var typed = new Credentials("使用者 ü", "密碼 🔑 \"quoted\" \\back");

        await store.SaveAsync(typed, ProfileId, CredentialKind.UserPassword);

        Assert.Equal(typed, await store.GetAsync(ProfileId, CredentialKind.UserPassword));
        await store.RemoveAsync(ProfileId, CredentialKind.UserPassword);
    }
}

/// <summary>The store for tests and for running without the Credential Manager.</summary>
public sealed class InMemoryCredentialStoreTests : CredentialStoreContract
{
    protected override ICredentialStore CreateStore() => new InMemoryCredentialStore();
}

/// <summary>
/// The Windows Credential Manager of the user who runs the tests, with a service name of its own that is
/// removed again, so that nothing of the app's is touched and nothing is left behind.
/// </summary>
public sealed class WindowsCredentialStoreTests : CredentialStoreContract, IAsyncLifetime
{
    private readonly string _service = "plaitway-cs-test-" + Guid.NewGuid().ToString("N")[..12];
    private readonly List<(string Profile, CredentialKind Kind)> _touched = [];

    protected override ICredentialStore CreateStore() => new WindowsCredentialStore(_service);

    public ValueTask InitializeAsync()
    {
        foreach (var profile in new[] { "profile-one", "another-profile", "other" })
        {
            _touched.Add((profile, CredentialKind.UserPassword));
            _touched.Add((profile, CredentialKind.KeyPassphrase));
        }

        return ValueTask.CompletedTask;
    }

    /// <summary>Removes what a failed test left, so that the vault is as it was.</summary>
    public async ValueTask DisposeAsync()
    {
        var store = new WindowsCredentialStore(_service);
        foreach (var (profile, kind) in _touched)
        {
            await store.RemoveAsync(profile, kind);
        }
    }

    [Fact]
    public async Task RefusesACredentialTooLargeForTheVault()
    {
        var store = new WindowsCredentialStore(_service);

        var error = await Assert.ThrowsAsync<CredentialStoreException>(
            () => store.SaveAsync(new Credentials("alice", new string('x', 4000)), "profile-one", CredentialKind.UserPassword));

        Assert.Equal("write", error.Operation);
        Assert.False(await store.ContainsAsync("profile-one", CredentialKind.UserPassword));
    }

    /// <summary>The JSON that goes to the vault holds the password in the clear; the copy in memory does not outlive the call.</summary>
    [Fact]
    public async Task WipesTheBlobItWrites()
    {
        var store = new WindowsCredentialStore(_service);
        var blob = System.Text.Encoding.UTF8.GetBytes("""{"username":"alice","password":"s3cret"}""");

        WindowsCredentialStore.WriteBlob(store.TargetName("profile-one", CredentialKind.UserPassword), blob);

        Assert.All(blob, value => Assert.Equal(0, value));
        Assert.Equal(new Credentials("alice", "s3cret"), await store.GetAsync("profile-one", CredentialKind.UserPassword));
        await store.RemoveAsync("profile-one", CredentialKind.UserPassword);
    }

    [Fact]
    public void WipesTheBlobItReads()
    {
        var blob = System.Text.Encoding.UTF8.GetBytes("""{"username":"alice","password":"s3cret"}""");

        var read = WindowsCredentialStore.Decode(blob);

        Assert.Equal(new Credentials("alice", "s3cret"), read);
        Assert.All(blob, value => Assert.Equal(0, value));
    }

    [Fact]
    public void WipesTheBlobEvenWhenItIsNotCredentials()
    {
        var blob = System.Text.Encoding.UTF8.GetBytes("not json at all, password s3cret");

        Assert.ThrowsAny<Exception>(() => WindowsCredentialStore.Decode(blob));

        Assert.All(blob, value => Assert.Equal(0, value));
    }

    [Fact]
    public void WipesTheBlobEvenWhenTheVaultRefusesIt()
    {
        var blob = new byte[CredentialNative.MaxBlobSize + 1];
        Array.Fill(blob, (byte)'x');

        Assert.Throws<CredentialStoreException>(() => WindowsCredentialStore.WriteBlob("plaitway-cs-test-never-written", blob));

        Assert.All(blob, value => Assert.Equal(0, value));
    }

    [Fact]
    public async Task StoresTheCredentialAsALocalMachineGenericCredential()
    {
        var store = new WindowsCredentialStore(_service);
        await store.SaveAsync(new Credentials("alice", "s3cret"), "profile-one", CredentialKind.UserPassword);

        // cmdkey lists the vault of the signed-in user: the credential is there under the target name.
        var listing = await RunAsync("cmdkey", "/list");

        Assert.Contains($"{_service}/profile-one:user-password", listing, StringComparison.Ordinal);
        AssertStoredAsLocalMachineGenericCredential($"{_service}/profile-one:user-password");
        await store.RemoveAsync("profile-one", CredentialKind.UserPassword);
    }

    /// <summary>
    /// Reads the credential back as Windows holds it: generic, and kept for this user on this machine
    /// (not for the logon session only, and not roamed).
    /// </summary>
    private static void AssertStoredAsLocalMachineGenericCredential(string target)
    {
        Assert.True(CredentialNative.CredRead(target, CredentialNative.TypeGeneric, 0, out var buffer));
        try
        {
            var credential = System.Runtime.InteropServices.Marshal.PtrToStructure<CredentialNative.Credential>(buffer);
            // The numbers of wincred.h, not the constants of the library: CRED_TYPE_GENERIC and CRED_PERSIST_LOCAL_MACHINE.
            Assert.Equal(1u, credential.Type);
            Assert.Equal(2u, credential.Persist);
        }
        finally
        {
            CredentialNative.CredFree(buffer);
        }
    }

    private static async Task<string> RunAsync(string file, string arguments)
    {
        var start = new System.Diagnostics.ProcessStartInfo(file, arguments) { RedirectStandardOutput = true, UseShellExecute = false, CreateNoWindow = true };
        using var process = System.Diagnostics.Process.Start(start)!;
        var output = await process.StandardOutput.ReadToEndAsync();
        await process.WaitForExitAsync();
        return output;
    }
}
