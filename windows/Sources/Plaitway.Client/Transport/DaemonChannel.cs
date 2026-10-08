using Grpc.Net.Client;

namespace Plaitway.Client.Transport;

/// <summary>Builds the gRPC channel that talks to the daemon over its named pipe.</summary>
internal static class DaemonChannel
{
    /// <summary>The authority gRPC puts in its requests. The pipe has no host name; this is a placeholder the daemon ignores.</summary>
    private const string Authority = "http://plaitway.pipe";

    /// <summary>
    /// A channel whose every connection is made by <paramref name="dialer"/>, so that no byte goes to a
    /// pipe the dialer has not verified. The transport is HTTP/2 without TLS, as the Go server speaks it.
    /// </summary>
    /// <remarks>
    /// There are no keepalive pings, on purpose. A named pipe reports a dead peer at once, so pings find
    /// nothing a read would not, and the daemon (grpc-go defaults: a ping at least every 5 minutes, none
    /// without a stream) counts each earlier ping as a strike and closes the connection with GOAWAY
    /// <c>too_many_pings</c> after the third. The channel owns its handler, so that disposing the channel
    /// closes the pipe; otherwise the daemon would keep the connection until the process ends.
    /// </remarks>
    public static GrpcChannel Create(PipePath pipe, PipeDialer dialer)
    {
        var handler = new SocketsHttpHandler
        {
            ConnectCallback = async (_, cancellationToken) => await dialer.ConnectAsync(pipe, cancellationToken).ConfigureAwait(false),
        };
        return GrpcChannel.ForAddress(Authority, new GrpcChannelOptions { HttpHandler = handler, DisposeHttpClient = true });
    }
}
