using Microsoft.UI.Dispatching;
using Plaitway.AppCore.Platform;

namespace Plaitway.App.Platform;

/// <summary><see cref="IUiScheduler"/> on the dispatcher queue of the window's thread.</summary>
/// <param name="queue">The queue of the UI thread.</param>
internal sealed class DispatcherScheduler(DispatcherQueue queue) : IUiScheduler
{
    /// <inheritdoc />
    public bool IsOnUiThread => queue.HasThreadAccess;

    /// <inheritdoc />
    public Task RunAsync(Action action)
    {
        if (queue.HasThreadAccess)
        {
            action();
            return Task.CompletedTask;
        }

        var done = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var queued = queue.TryEnqueue(() =>
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
        });
        if (!queued)
        {
            done.SetException(new InvalidOperationException("the UI thread is shutting down"));
        }

        return done.Task;
    }
}
