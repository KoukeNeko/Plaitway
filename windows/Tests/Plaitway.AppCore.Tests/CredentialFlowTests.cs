using Grpc.Core;
using Plaitway.AppCore.Daemon;
using Plaitway.AppCore.Tests.Support;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.AppCore.Tests;

/// <summary>A credential store that is slow to remove an item, like a vault that stops to ask.</summary>
internal sealed class SlowRemovalStore : ICredentialStore
{
    private static readonly TimeSpan RemovalDelay = TimeSpan.FromMilliseconds(300);

    private readonly InMemoryCredentialStore _items = new();

    public Task<Credentials?> GetAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        _items.GetAsync(profileId, kind, cancellationToken);

    public Task<bool> ContainsAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        _items.ContainsAsync(profileId, kind, cancellationToken);

    public Task SaveAsync(Credentials credentials, string profileId, CredentialKind kind, CancellationToken cancellationToken = default) =>
        _items.SaveAsync(credentials, profileId, kind, cancellationToken);

    public async Task RemoveAsync(string profileId, CredentialKind kind, CancellationToken cancellationToken = default)
    {
        await Task.Delay(RemovalDelay, cancellationToken);
        await _items.RemoveAsync(profileId, kind, cancellationToken);
    }
}

/// <summary>macos/Tests/PlaitwayTests/Client/CredentialFlowTests.swift: what the store asks and answers, against the real daemon.</summary>
public sealed class CredentialFlowTests(DaemonBinary binary)
{
    private static readonly byte[] NeedsCredentials = Fixture.OpenVpn(markers: ["# fake: needs-credentials"]);

    [Fact]
    public async Task AsksOnceAndThenAnswersFromTheCredentialStore()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);

            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the credential prompt", () => app.Store.CredentialPrompts.Count > 0);
            var prompt = app.Store.CredentialPrompts[0];
            Assert.Equal(id, prompt.ProfileId);
            Assert.Equal(CredentialKind.UserPassword, prompt.Kind);
            Assert.False(prompt.Rejected);

            await app.Store.ProvideCredentialsAsync(id, "alice", "s3cret");
            await app.WaitForStateAsync(id, ProfileState.Connected);
            Assert.Empty(app.Store.CredentialPrompts);
            Assert.Equal(new Credentials("alice", "s3cret"), await saved.GetAsync(id, CredentialKind.UserPassword));

            // The next connection needs no prompt: the store answers by itself.
            await app.Store.SetEnabledAsync(id, enabled: false);
            await app.WaitForStateAsync(id, ProfileState.Disconnected);
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the reconnection", () =>
            {
                Assert.Empty(app.Store.CredentialPrompts);
                return app.Store.Find(id)?.State == ProfileState.Connected;
            });
        });
    }

    [Fact]
    public async Task AsksAgainWhenTheDaemonRefusesWhatWasTyped()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the credential prompt", () => app.Store.CredentialPrompts.Count > 0);

            // The fake daemon refuses the password "wrong" once.
            await app.Store.ProvideCredentialsAsync(id, "alice", "wrong");
            await Wait.UntilAsync("the prompt to come back", () => app.Store.CredentialPrompts is [{ Rejected: true }, ..]);
            Assert.Equal("authentication failed", app.Store.CredentialPrompts[0].Message);
            Assert.Null(await saved.GetAsync(id, CredentialKind.UserPassword));

            await app.Store.ProvideCredentialsAsync(id, "alice", "right");
            await app.WaitForStateAsync(id, ProfileState.Connected);
            Assert.Empty(app.Store.CredentialPrompts);
            Assert.Equal("right", (await saved.GetAsync(id, CredentialKind.UserPassword))?.Password);
        });
    }

    [Fact]
    public async Task ForgetsSavedCredentialsThatTheDaemonRefusesAndAsks()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await saved.SaveAsync(new Credentials("alice", "wrong"), id, CredentialKind.UserPassword);

            await app.Store.SetEnabledAsync(id, enabled: true);

            // The saved password is sent without asking, refused, and then the prompt appears.
            await Wait.UntilAsync("the prompt after the refusal", () => app.Store.CredentialPrompts is [{ Rejected: true }, ..]);
            Assert.Null(await saved.GetAsync(id, CredentialKind.UserPassword));
        });
    }

    [Fact]
    public async Task UsesCredentialsSavedBeforeTheFirstConnection()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);

            await app.Store.SetEnabledAsync(id, enabled: true);

            await Wait.UntilAsync("the connection", () =>
            {
                Assert.Empty(app.Store.CredentialPrompts);
                return app.Store.Find(id)?.State == ProfileState.Connected;
            });
        });
    }

    [Fact]
    public async Task CancellingThePromptStopsConnecting()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the credential prompt", () => app.Store.CredentialPrompts.Count > 0);

            await app.Store.CancelCredentialsAsync(id);

            await app.WaitForStateAsync(id, ProfileState.Disconnected);
            Assert.Empty(app.Store.CredentialPrompts);
            Assert.False(app.Store.Find(id)?.DesiredEnabled);
        });
    }

    [Fact]
    public async Task ThePromptGoesAwayWhenSomeoneElseDisconnects()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the credential prompt", () => app.Store.CredentialPrompts.Count > 0);

            await app.Store.SetEnabledAsync(id, enabled: false);

            await Wait.UntilAsync("the prompt to go", () => app.Store.CredentialPrompts.Count == 0);
        });
    }

    [Fact]
    public async Task ThePromptGoesAwayWithTheDaemon()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await app.Store.SetEnabledAsync(id, enabled: true);
            await Wait.UntilAsync("the credential prompt", () => app.Store.CredentialPrompts.Count > 0);

            app.Daemon.Kill();

            await Wait.UntilAsync("the outage", () => app.Store.Connection == DaemonConnection.Unavailable);
            Assert.Empty(app.Store.CredentialPrompts);
        });
    }

    [Fact]
    public async Task AnsweringWithoutARequestIsRefused()
    {
        await using var app = await AppHarness.StartAsync(binary);
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn");

            var error = await Assert.ThrowsAsync<RpcException>(() => app.Store.ProvideCredentialsAsync(id, "alice", "s3cret"));

            Assert.Equal(StatusCode.FailedPrecondition, error.StatusCode);
        });
    }

    [Fact]
    public async Task DeletingAProfileForgetsItsCredentials()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);
            await saved.SaveAsync(new Credentials("phrase"), id, CredentialKind.KeyPassphrase);
            var other = await app.ImportFixtureAsync("other.ovpn");
            await saved.SaveAsync(new Credentials("bob", "pw"), other, CredentialKind.UserPassword);

            await app.Store.DeleteProfileAsync(id);

            await Wait.UntilAsync("the profile to go", () => app.Store.Find(id) is null);
            Assert.False(await saved.ContainsAsync(id, CredentialKind.UserPassword));
            Assert.False(await saved.ContainsAsync(id, CredentialKind.KeyPassphrase));
            Assert.NotNull(await saved.GetAsync(other, CredentialKind.UserPassword));
        });
    }

    [Fact]
    public async Task TheProfileWatchReportingARemovalAloneForgetsItsCredentials()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn");
            await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);

            // Deleted behind the store's back, as another client of the daemon would.
            await app.Api.DeleteProfileAsync(id);

            await Wait.UntilAsync("the saved credentials to go", async () => !await saved.ContainsAsync(id, CredentialKind.UserPassword));
        });
    }

    [Fact]
    public async Task DeletingAProfileForgetsItsCredentialsBeforeItReturns()
    {
        // The removed event may never arrive (the watch is down at that moment), so the delete itself must leave
        // nothing behind in the credential store.
        var saved = new SlowRemovalStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);
            await saved.SaveAsync(new Credentials("phrase"), id, CredentialKind.KeyPassphrase);

            await app.Store.DeleteProfileAsync(id);

            Assert.False(await saved.ContainsAsync(id, CredentialKind.UserPassword));
            Assert.False(await saved.ContainsAsync(id, CredentialKind.KeyPassphrase));
        });
    }

    [Fact]
    public async Task SavedCredentialsCanBeForgotten()
    {
        var saved = new InMemoryCredentialStore();
        await using var app = await AppHarness.StartAsync(binary, new HarnessOptions { Credentials = saved });
        await app.RunAsync(async () =>
        {
            var id = await app.ImportFixtureAsync("router.ovpn", NeedsCredentials);
            Assert.False(await app.Store.HasSavedCredentialsAsync(id, CredentialKind.UserPassword));
            await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);
            Assert.True(await app.Store.HasSavedCredentialsAsync(id, CredentialKind.UserPassword));
            Assert.False(await app.Store.HasSavedCredentialsAsync(id, CredentialKind.KeyPassphrase));

            await app.Store.ForgetSavedCredentialsAsync(id);

            Assert.False(await app.Store.HasSavedCredentialsAsync(id, CredentialKind.UserPassword));
        });
    }
}
