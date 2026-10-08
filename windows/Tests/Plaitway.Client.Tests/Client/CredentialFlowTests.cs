using Grpc.Core;
using Plaitway.Client.Storage;
using Plaitway.Client.Tests.Support;
using Plaitway.V1;

namespace Plaitway.Client.Tests.Client;

/// <summary>
/// macos/Tests/PlaitwayTests/Client/CredentialFlowTests.swift, as far as it belongs to the client library:
/// the daemon's requests and the answers to them. Which answer to send, and when to ask the user, is the
/// app's model (ProfileStore+Credentials.swift on macOS).
/// </summary>
public sealed class CredentialFlowTests(DaemonBinary binary)
{
    private static readonly byte[] NeedsCredentials = Fixture.OpenVpn(markers: ["# fake: needs-credentials"]);

    [Fact]
    public async Task TheDaemonAsksAndTheSavedAnswerIsSentWithoutAskingAgain()
    {
        var saved = new InMemoryCredentialStore();
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("router.ovpn", NeedsCredentials);

        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.AwaitingCredentials);
        Assert.Equal(CredentialKind.UserPassword, session.Profile(id)!.CredentialRequest.Kind);

        var typed = new Credentials("alice", "s3cret");
        await saved.SaveAsync(typed, id, CredentialKind.UserPassword);
        await session.Client.ProvideCredentialsAsync(id, CredentialKind.UserPassword, typed);
        await session.WaitForStateAsync(id, ProfileState.Connected);

        // The next connection asks again, because the daemon keeps nothing; the saved answer serves.
        await session.Client.SetProfileEnabledAsync(id, false);
        await session.WaitForStateAsync(id, ProfileState.Disconnected);
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.AwaitingCredentials);
        var answer = await saved.GetAsync(id, CredentialKind.UserPassword);
        await session.Client.ProvideCredentialsAsync(id, CredentialKind.UserPassword, answer!);
        await session.WaitForStateAsync(id, ProfileState.Connected);
    }

    [Fact]
    public async Task AskedAgainWhenTheDaemonRefusesWhatWasSent()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("router.ovpn", NeedsCredentials);
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.AwaitingCredentials);

        // The fake daemon refuses the password "wrong" once.
        await session.Client.ProvideCredentialsAsync(id, CredentialKind.UserPassword, new Credentials("alice", "wrong"));
        await DaemonSession.WaitUntilAsync(
            "the request to come back refused",
            () => session.Profile(id) is { State: ProfileState.AwaitingCredentials, LastError: "authentication failed" });

        await session.Client.ProvideCredentialsAsync(id, CredentialKind.UserPassword, new Credentials("alice", "right"));
        await session.WaitForStateAsync(id, ProfileState.Connected);
    }

    [Fact]
    public async Task AnswerSavedBeforeTheFirstConnectionServesTheFirstRequest()
    {
        var saved = new InMemoryCredentialStore();
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("router.ovpn", NeedsCredentials);
        await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);

        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.AwaitingCredentials);
        await session.Client.ProvideCredentialsAsync(id, CredentialKind.UserPassword, (await saved.GetAsync(id, CredentialKind.UserPassword))!);

        await session.WaitForStateAsync(id, ProfileState.Connected);
    }

    [Fact]
    public async Task CancellingTheSheetStopsConnecting()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("router.ovpn", NeedsCredentials);
        await session.Client.SetProfileEnabledAsync(id, true);
        await session.WaitForStateAsync(id, ProfileState.AwaitingCredentials);

        await session.Client.SetProfileEnabledAsync(id, false);

        await session.WaitForStateAsync(id, ProfileState.Disconnected);
        Assert.False(session.Profile(id)!.DesiredEnabled);
    }

    [Fact]
    public async Task AnsweringWithoutARequestIsRefused()
    {
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("router.ovpn");

        var error = await Assert.ThrowsAsync<RpcException>(
            () => session.Client.ProvideCredentialsAsync(id, CredentialKind.UserPassword, new Credentials("alice", "s3cret")));

        Assert.Equal(StatusCode.FailedPrecondition, error.StatusCode);
    }

    [Fact]
    public async Task ADeletedProfilesCredentialsCanBeForgottenAndOthersStay()
    {
        var saved = new InMemoryCredentialStore();
        await using var session = await DaemonSession.StartAsync(binary);
        var id = await session.ImportFixtureAsync("router.ovpn", NeedsCredentials);
        var other = await session.ImportFixtureAsync("other.ovpn");
        await saved.SaveAsync(new Credentials("alice", "s3cret"), id, CredentialKind.UserPassword);
        await saved.SaveAsync(new Credentials("phrase"), id, CredentialKind.KeyPassphrase);
        await saved.SaveAsync(new Credentials("bob", "pw"), other, CredentialKind.UserPassword);

        await session.Client.DeleteProfileAsync(id);
        await saved.ForgetProfileAsync(id);

        Assert.False(await saved.ContainsAsync(id, CredentialKind.UserPassword));
        Assert.False(await saved.ContainsAsync(id, CredentialKind.KeyPassphrase));
        Assert.NotNull(await saved.GetAsync(other, CredentialKind.UserPassword));
    }
}
