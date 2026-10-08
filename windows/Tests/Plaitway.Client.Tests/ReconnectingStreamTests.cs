using Grpc.Core;
using Plaitway.V1;

namespace Plaitway.Client.Tests;

/// <summary>The reconnecting stream, with calls whose messages and failures the test scripts.</summary>
public sealed class ReconnectingStreamTests
{
    private static readonly BackoffPolicy NoWait = new(TimeSpan.Zero, TimeSpan.Zero, 1, 0);

    /// <summary>Delivers the script in order: a <typeparamref name="T"/> is a message, an <see cref="Exception"/> is thrown, the end is the end of the stream.</summary>
    private sealed class ScriptedReader<T>(IEnumerable<object> script) : IAsyncStreamReader<T>
    {
        private readonly IEnumerator<object> _script = script.GetEnumerator();

        public T Current { get; private set; } = default!;

        public Task<bool> MoveNext(CancellationToken cancellationToken)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (!_script.MoveNext())
            {
                return Task.FromResult(false);
            }

            if (_script.Current is Exception error)
            {
                return Task.FromException<bool>(error);
            }

            Current = (T)_script.Current;
            return Task.FromResult(true);
        }
    }

    private static AsyncServerStreamingCall<T> Call<T>(Action onDispose, params object[] script) =>
        new(new ScriptedReader<T>(script), Task.FromResult(new Metadata()), () => Status.DefaultSuccess, () => [], onDispose);

    private static RpcException Unavailable() => new(new Status(StatusCode.Unavailable, "gone"));

    private static LogLine Line(string text) => new() { Text = text };

    [Fact]
    public async Task ReportsAnOutageOnceThenReopensAndReportsAgainAfterAMessage()
    {
        var calls = new Queue<object[]>(
        [
            [Line("a"), Unavailable()],
            [Unavailable()],
            [Line("b")],
            [Line("c"), Unavailable()],
        ]);
        var opened = 0;
        var disposed = 0;
        using var stop = new CancellationTokenSource();
        var seen = new List<string>();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(async () =>
        {
            await foreach (var item in ReconnectingStream.ReadAsync(
                _ =>
                {
                    opened++;
                    if (calls.Count == 0)
                    {
                        stop.Cancel();
                        stop.Token.ThrowIfCancellationRequested();
                    }

                    return Call<LogLine>(() => disposed++, calls.Dequeue());
                },
                NoWait,
                () => 0.5,
                stop.Token))
            {
                seen.Add(item.Outage is not null ? "outage" : item.Message!.Text);
            }
        });

        // The second failure comes with no message in between: it is the same outage. "b" ends the stream
        // normally, which is a loss too.
        Assert.Equal(["a", "outage", "b", "outage", "c", "outage"], seen);
        Assert.Equal(5, opened);
        Assert.Equal(4, disposed);
    }

    private static LogLine At(long seconds, int nanos, string text) => new()
    {
        Text = text,
        Time = new Google.Protobuf.WellKnownTypes.Timestamp { Seconds = seconds, Nanos = nanos },
    };

    /// <summary>
    /// The daemon stamps lines with a clock that ticks slower than it logs, so many lines share a time. Lines
    /// before the loss are all delivered; the tail that the new stream starts with repeats some of them
    /// (the tail is cut in the middle of a group that shares a time) and adds lines.
    /// </summary>
    [Fact]
    public async Task ALogStreamThatIsOpenedAgainDeliversEachLineOnce()
    {
        var calls = new Queue<object[]>(
        [
            [At(1, 0, "a"), At(1, 0, "b"), At(1, 0, "c"), At(2, 0, "d"), At(2, 0, "e"), Unavailable()],
            [At(1, 0, "c"), At(2, 0, "d"), At(2, 0, "e"), At(2, 0, "f"), At(2, 0, "g"), At(3, 0, "h"), Unavailable()],
            [At(2, 0, "e"), At(2, 0, "f"), At(2, 0, "g"), At(3, 0, "h"), At(3, 0, "i")],
        ]);
        using var stop = new CancellationTokenSource();
        var delivered = new List<string>();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(async () =>
        {
            var items = ReconnectingStream.ReadAsync(
                _ =>
                {
                    if (calls.Count == 0)
                    {
                        stop.Cancel();
                        stop.Token.ThrowIfCancellationRequested();
                    }

                    return Call<LogLine>(() => { }, calls.Dequeue());
                },
                NoWait,
                () => 0.5,
                stop.Token);
            await foreach (var line in DaemonClient.WithoutRepeats(items, stop.Token))
            {
                delivered.Add(line.Text);
            }
        });

        Assert.Equal(["a", "b", "c", "d", "e", "f", "g", "h", "i"], delivered);
    }

    [Fact]
    public async Task AFailureThatRetryingCannotMendIsThrown()
    {
        var error = new RpcException(new Status(StatusCode.NotFound, "no such profile"));
        var opened = 0;

        var thrown = await Assert.ThrowsAsync<RpcException>(async () =>
        {
            await foreach (var item in ReconnectingStream.ReadAsync(
                _ =>
                {
                    opened++;
                    return Call<LogLine>(() => { }, error);
                },
                NoWait,
                () => 0.5,
                CancellationToken.None,
                isPermanent: failure => failure is RpcException { StatusCode: StatusCode.NotFound }))
            {
                Assert.Fail($"got {item}");
            }
        });

        Assert.Same(error, thrown);
        Assert.Equal(1, opened);
    }

    [Fact]
    public async Task AnErrorThatIsNotATransportFailureIsNotRetried()
    {
        var opened = 0;

        await Assert.ThrowsAsync<InvalidOperationException>(async () =>
        {
            await foreach (var item in ReconnectingStream.ReadAsync(
                _ =>
                {
                    opened++;
                    return Call<LogLine>(() => { }, new InvalidOperationException("a bug"));
                },
                NoWait,
                () => 0.5,
                CancellationToken.None))
            {
                Assert.Fail($"got {item}");
            }
        });

        Assert.Equal(1, opened);
    }

    [Fact]
    public async Task CancellationDuringAReadEndsWithOperationCanceled()
    {
        using var stop = new CancellationTokenSource();
        var disposed = 0;
        // After its first message the call fails the way gRPC fails a call that was cancelled: with an
        // RpcException of status Cancelled, whatever the reader was asked.
        var cancelled = new RpcException(new Status(StatusCode.Cancelled, "Call canceled by the client."));
        await using var items = ReconnectingStream.ReadAsync(
            _ => Call<LogLine>(() => disposed++, Line("a"), cancelled),
            NoWait,
            () => 0.5,
            stop.Token).GetAsyncEnumerator(stop.Token);
        Assert.True(await items.MoveNextAsync());

        await stop.CancelAsync();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(async () => await items.MoveNextAsync());
    }
}
