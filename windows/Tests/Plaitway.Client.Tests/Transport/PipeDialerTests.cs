using System.IO.Pipes;
using System.Security.Principal;
using Grpc.Core;
using Plaitway.Client.Tests.Support;
using Plaitway.Client.Transport;

namespace Plaitway.Client.Tests.Transport;

/// <summary>
/// The client side of internal/transport (serverowner_windows_test.go, transport_windows_test.go) against a
/// pipe that this process serves with the daemon's access list and an owner of its choosing.
/// </summary>
public sealed class PipeDialerTests
{
    private const string OtherUserSid = "S-1-5-21-1111111111-2222222222-3333333333-1002";

    private static readonly TimeSpan ShortConnectTimeout = TimeSpan.FromSeconds(3);

    private static SecurityIdentifier OtherUser { get; } = new(OtherUserSid);

    private static async Task<Exception> RefusalOfAsync(PipeDialer dialer, PipePath pipe) =>
        await Assert.ThrowsAnyAsync<Exception>(async () => (await dialer.ConnectAsync(pipe, CancellationToken.None)).Dispose());

    [Fact]
    public async Task ConnectsToAPipeOwnedByTheCaller()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());

        await using var connection = await new PipeDialer(connectTimeout: ShortConnectTimeout).ConnectAsync(server.Pipe, CancellationToken.None);
        await server.WaitForConnectionAsync();

        Assert.True(connection.CanWrite);
    }

    [Fact]
    public async Task OpensThePipeWithExactlyTheAccessMaskOfTheGoClientAtIdentificationLevel()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());

        await using var connection = await new PipeDialer(connectTimeout: ShortConnectTimeout).ConnectAsync(server.Pipe, CancellationToken.None);

        var pipe = Assert.IsType<NamedPipeClientStream>(connection);
        // FILE_READ_DATA | FILE_WRITE_DATA | FILE_READ_ATTRIBUTES | READ_CONTROL | SYNCHRONIZE, and not FILE_APPEND_DATA,
        // which on a pipe is FILE_CREATE_PIPE_INSTANCE.
        const uint goMask = 0x1 | 0x2 | 0x80 | 0x20000 | 0x100000;
        Assert.Equal(goMask, (uint)PipeDialer.ClientAccess);
        Assert.Equal(goMask, TestNative.GrantedAccess(pipe.SafePipeHandle));
        Assert.Equal(0u, TestNative.GrantedAccess(pipe.SafePipeHandle) & 0x4);
       Assert.True(pipe.IsAsync);
    }

    [Fact]
    public async Task RefusesAServerOwnedBySomeoneElseAndSendsNothing()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        var dialer = new PipeDialer(() => OtherUser, ShortConnectTimeout);

        var error = Assert.IsType<PipeServerRefusedException>(await RefusalOfAsync(dialer, server.Pipe));

        Assert.StartsWith(PipeServerRefusedException.Prefix, error.Message, StringComparison.Ordinal);
        Assert.Contains(TestPipeServer.CurrentUser().Value, error.Message, StringComparison.Ordinal);
        // The refused client hung up without a byte: the server sees end of file.
        await server.WaitForConnectionAsync();
        await Task.Delay(TimeSpan.FromMilliseconds(300));
        Assert.Equal(0, server.BytesReceived);
    }

    [Fact]
    public async Task AGrpcCallRefusesAServerOwnedBySomeoneElseBeforeAnyRequestIsSent()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        await using var client = DaemonClient.ConnectAs(server.Pipe, () => OtherUser, ShortConnectTimeout);

        var error = await Assert.ThrowsAsync<RpcException>(() => client.GetDaemonInfoAsync());

        Assert.Equal(StatusCode.Unavailable, error.StatusCode);
        var failure = DaemonFailure.From(error);
        Assert.Equal(DaemonFailureKind.ServerRefused, failure.Kind);
        Assert.StartsWith(PipeServerRefusedException.Prefix, failure.Message, StringComparison.Ordinal);
        await server.WaitForConnectionAsync();
        await Task.Delay(TimeSpan.FromMilliseconds(300));
        Assert.Equal(0, server.BytesReceived);
    }

    [Fact]
    public async Task AGrpcCallSendsItsRequestToAServerOwnedByTheCaller()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        await using var client = DaemonClient.ConnectAs(server.Pipe, TestPipeServer.CurrentUser, ShortConnectTimeout);
        using var cancel = new CancellationTokenSource(TimeSpan.FromSeconds(2));

        // The server only counts bytes and never answers, so the call ends by its deadline: what matters is
        // that the HTTP/2 preface reached a server that passed the check.
        await Assert.ThrowsAnyAsync<Exception>(() => client.GetDaemonInfoAsync(cancel.Token));

        Assert.True(server.BytesReceived > 0, "nothing was sent to a server owned by the caller");
    }

    [Fact]
    public async Task FailsClosedWhenTheCallerCannotBeIdentified()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        var dialer = new PipeDialer(() => throw new InvalidOperationException("token unreadable"), ShortConnectTimeout);

        var error = Assert.IsType<PipeServerRefusedException>(await RefusalOfAsync(dialer, server.Pipe));

        Assert.Contains("token unreadable", error.Message, StringComparison.Ordinal);
        Assert.Equal(0, server.Connections);
    }

    [Fact]
    public async Task FailsClosedWhenTheOwnerCannotBeRead()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        // A handle opened without READ_CONTROL cannot be asked for its owner.
        await using var pipe = new NamedPipeClientStream(
            ".",
            server.PipeName,
            PipeDialer.ClientAccess & ~PipeAccessRights.ReadPermissions,
            PipeOptions.Asynchronous,
            TokenImpersonationLevel.Identification,
            HandleInheritability.None);
        await pipe.ConnectAsync(3000);

        var error = Assert.Throws<PipeServerRefusedException>(() => PipeServerVerifier.Verify(pipe, TestPipeServer.CurrentUser()));

        Assert.StartsWith(PipeServerRefusedException.Prefix, error.Message, StringComparison.Ordinal);
        Assert.Contains("cannot read its owner", error.Message, StringComparison.Ordinal);
    }

    [Fact]
    public async Task APipeThatDoesNotExistIsUnavailableNotRefused()
    {
        var missing = PipePath.Parse(PipePath.Namespace + "plaitway-cs-test-missing-" + Guid.NewGuid().ToString("N")[..8]);
        await using var client = DaemonClient.ConnectAs(missing, TestPipeServer.CurrentUser, TimeSpan.FromMilliseconds(200));

        var error = await Assert.ThrowsAsync<RpcException>(() => client.GetDaemonInfoAsync());

        Assert.Equal(StatusCode.Unavailable, error.StatusCode);
        Assert.Equal(DaemonFailureKind.Unavailable, DaemonFailure.From(error).Kind);
    }

    [Fact]
    public async Task APipeThatRefusesTheUserIsPermissionDeniedNotUnavailable()
    {
        await using var server = TestPipeServer.Refusing();
        await using var client = DaemonClient.ConnectAs(server.Pipe, TestPipeServer.CurrentUser, ShortConnectTimeout);

        var error = await Assert.ThrowsAsync<RpcException>(() => client.GetDaemonInfoAsync());

        Assert.Equal(DaemonFailureKind.PermissionDenied, DaemonFailure.From(error).Kind);
        Assert.Equal(0, server.Connections);
    }

    /// <summary>Every user but the owner reaches the pipe through the Interactive entry of its access list, and only with this mask.</summary>
    [Fact]
    public async Task AnOrdinaryInteractiveUserCanConnectAndReadTheOwnerButNotAskForGenericWrite()
    {
        await using var server = TestPipeServer.OwnedBy(TestPipeServer.CurrentUser());
        var owner = TestPipeServer.CurrentUser();
        Exception? failure = null;
        var denied = false;

        TestNative.AsOrdinaryInteractiveUser(() =>
        {
            try
            {
                using var generic = new NamedPipeClientStream(".", server.PipeName, PipeDirection.InOut, PipeOptions.None, TokenImpersonationLevel.Identification);
                try
                {
                    generic.Connect(3000);
                }
                catch (UnauthorizedAccessException)
                {
                    denied = true;
                }

                using var exact = new NamedPipeClientStream(
                    ".", server.PipeName, PipeDialer.ClientAccess, PipeOptions.Asynchronous, TokenImpersonationLevel.Identification, HandleInheritability.None);
                exact.Connect(3000);
                PipeServerVerifier.Verify(exact, owner);
            }
            catch (Exception error)
            {
                failure = error;
            }
        });

        Assert.Null(failure);
        Assert.True(denied, "an interactive user opening with generic read/write was not refused");
    }
}
