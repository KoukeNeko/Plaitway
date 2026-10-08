namespace Plaitway.AppCore.Tests.Support;

/// <summary>Waiting for the daemon to say what it did, which comes back through the watch.</summary>
internal static class Wait
{
    private static readonly TimeSpan DefaultTimeout = TimeSpan.FromSeconds(15);
    private static readonly TimeSpan PollInterval = TimeSpan.FromMilliseconds(20);

    /// <summary>Polls <paramref name="condition"/> on the calling thread, which is the UI thread inside a test body, until it holds.</summary>
    /// <exception cref="TimeoutException">It did not hold in time.</exception>
    public static Task UntilAsync(string what, Func<bool> condition, TimeSpan? timeout = null) =>
        UntilAsync(what, () => Task.FromResult(condition()), timeout);

    /// <summary>As <see cref="UntilAsync(string, Func{bool}, TimeSpan?)"/>, for a condition that has to await something.</summary>
    public static async Task UntilAsync(string what, Func<Task<bool>> condition, TimeSpan? timeout = null)
    {
        var deadline = DateTime.UtcNow + (timeout ?? DefaultTimeout);
        while (!await condition())
        {
            if (DateTime.UtcNow > deadline)
            {
                throw new TimeoutException($"timed out waiting for {what}");
            }

            await Task.Delay(PollInterval);
        }
    }
}
