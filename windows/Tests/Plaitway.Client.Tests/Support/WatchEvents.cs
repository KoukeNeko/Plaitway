using Plaitway.V1;

namespace Plaitway.Client.Tests.Support;

/// <summary>What a test records from <see cref="DaemonClient.WatchProfilesAsync"/>; the list is also written by the task that reads the watch.</summary>
internal static class WatchEvents
{
    /// <summary>How many of the events are a <typeparamref name="T"/>.</summary>
    public static int Count<T>(List<ProfileWatchEvent> events)
    {
        lock (events)
        {
            return events.OfType<T>().Count();
        }
    }
}
