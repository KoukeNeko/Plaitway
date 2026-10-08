using System.Collections.Concurrent;
using Plaitway.AppCore.Platform;

namespace Plaitway.AppCore.Tests.Support;

/// <summary>
/// The UI thread of the tests: one thread that runs what it is given, one item at a time, and brings every
/// <c>await</c> of that work back to itself, as the dispatcher of a window does. The tests run their bodies on it,
/// which is what the Swift tests get from <c>@MainActor</c>.
/// </summary>
public sealed class UiThread : IUiScheduler, IDisposable
{
    private readonly BlockingCollection<(SendOrPostCallback Callback, object? State)> _queue = [];
    private readonly Thread _thread;
    private readonly UiContext _context;

    public UiThread()
    {
        _context = new UiContext(this);
        _thread = new Thread(Loop) { IsBackground = true, Name = "test UI thread" };
        _thread.Start();
    }

    /// <inheritdoc />
    public bool IsOnUiThread => Environment.CurrentManagedThreadId == _thread.ManagedThreadId;

    /// <inheritdoc />
    public Task RunAsync(Action action)
    {
        var done = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _queue.Add((_ =>
        {
            try
            {
                action();
                done.SetResult();
            }
            catch (Exception error)
            {
                done.SetException(error);
            }
        }, null));
        return done.Task;
    }

    /// <summary>Runs <paramref name="body"/> on the thread, awaits included, and completes when it does.</summary>
    public Task RunAsync(Func<Task> body)
    {
        var done = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        _queue.Add((async _ =>
        {
            try
            {
                await body();
                done.SetResult();
            }
            catch (Exception error)
            {
                done.SetException(error);
            }
        }, null));
        return done.Task;
    }

    public void Dispose()
    {
        _queue.CompleteAdding();
        _thread.Join();
        _queue.Dispose();
    }

    private void Loop()
    {
        SynchronizationContext.SetSynchronizationContext(_context);
        foreach (var (callback, state) in _queue.GetConsumingEnumerable())
        {
            callback(state);
        }
    }

    private sealed class UiContext(UiThread owner) : SynchronizationContext
    {
        public override void Post(SendOrPostCallback callback, object? state)
        {
            try
            {
                owner._queue.Add((callback, state));
            }
            catch (Exception error) when (error is ObjectDisposedException or InvalidOperationException)
            {
                // The thread is gone, as a dispatcher is when its window closed: what comes back to it is dropped.
            }
        }

        public override void Send(SendOrPostCallback callback, object? state)
        {
            if (owner.IsOnUiThread)
            {
                callback(state);
                return;
            }

            using var done = new ManualResetEventSlim();
            Post(_ =>
            {
                callback(state);
                done.Set();
            }, null);
            done.Wait();
        }

        public override SynchronizationContext CreateCopy() => this;
    }
}
