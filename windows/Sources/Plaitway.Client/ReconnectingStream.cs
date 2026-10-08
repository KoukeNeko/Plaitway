using System.Runtime.CompilerServices;
using Grpc.Core;

namespace Plaitway.Client;

/// <summary>One thing a reconnecting stream produced: a message, or the report that the connection was lost.</summary>
/// <typeparam name="T">The message type of the stream.</typeparam>
/// <param name="Message">The message; null for an outage report.</param>
/// <param name="Outage">Why the stream was lost; null for a message.</param>
internal readonly record struct StreamItem<T>(T? Message, Exception? Outage)
    where T : class;

/// <summary>
/// Reads a server-streaming call and, when it breaks or ends, opens it again after the backoff. The
/// daemon being down or refusing us looks the same to the caller, so the cause is reported once per
/// outage (until a message arrives again).
/// </summary>
internal static class ReconnectingStream
{
    public static async IAsyncEnumerable<StreamItem<T>> ReadAsync<T>(
        Func<CancellationToken, AsyncServerStreamingCall<T>> open,
        BackoffPolicy backoff,
        Func<double> random,
        [EnumeratorCancellation] CancellationToken cancellationToken,
        Func<Exception, bool>? isPermanent = null)
        where T : class
    {
        var failures = 0;
        var outageReported = false;
        while (true)
        {
            cancellationToken.ThrowIfCancellationRequested();
            using var call = open(cancellationToken);
            Exception? lost = null;
            while (lost is null)
            {
                var read = await ReadNextAsync(call.ResponseStream, cancellationToken).ConfigureAwait(false);
                if (read.Message is not null)
                {
                    failures = 0;
                    outageReported = false;
                    yield return new StreamItem<T>(read.Message, null);
                }
                else
                {
                    lost = read.Failure ?? new IOException("the stream ended");
                }
            }

            if (isPermanent?.Invoke(lost) == true)
            {
                throw lost;
            }

            failures++;
            if (!outageReported)
            {
                outageReported = true;
                yield return new StreamItem<T>(null, lost);
            }

            await Task.Delay(backoff.Delay(failures, random()), cancellationToken).ConfigureAwait(false);
        }
    }

    private static async Task<(T? Message, Exception? Failure)> ReadNextAsync<T>(IAsyncStreamReader<T> reader, CancellationToken cancellationToken)
        where T : class
    {
        try
        {
            return await reader.MoveNext(cancellationToken).ConfigureAwait(false) ? (reader.Current, null) : (null, null);
        }
        catch (Exception) when (cancellationToken.IsCancellationRequested)
        {
            // gRPC reports its own cancellation as an RpcException; the caller asked for it, so it ends
            // the sequence the way cancellation does everywhere else.
            throw new OperationCanceledException(cancellationToken);
        }
        catch (Exception error) when (IsTransportFailure(error))
        {
            return (null, error);
        }
    }

    /// <summary>What a broken connection, a missing pipe or a refused server throws; anything else is a bug and propagates.</summary>
    private static bool IsTransportFailure(Exception error) => error is RpcException or IOException or HttpRequestException or ObjectDisposedException;
}
