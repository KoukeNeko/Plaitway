using Microsoft.UI.Dispatching;

namespace Plaitway.App.Controls;

/// <summary>Calls something once a second while a view is on screen, such as the uptime that counts up.</summary>
internal sealed class DispatcherTimerClock
{
    private static readonly TimeSpan Interval = TimeSpan.FromSeconds(1);

    private readonly DispatcherQueueTimer _timer;

    public DispatcherTimerClock(DispatcherQueue queue, Action tick)
    {
        _timer = queue.CreateTimer();
        _timer.Interval = Interval;
        _timer.IsRepeating = true;
        _timer.Tick += (_, _) => tick();
    }

    public void Start() => _timer.Start();

    public void Stop() => _timer.Stop();
}
