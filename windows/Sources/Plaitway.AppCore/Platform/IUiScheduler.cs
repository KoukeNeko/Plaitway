namespace Plaitway.AppCore.Platform;

/// <summary>
/// The thread that owns the models and the view models. Everything they hold is touched on it only; work that
/// starts elsewhere (a watch that reads the pipe) hands its result over with <see cref="RunAsync"/>. WinUI's
/// implementation posts to the dispatcher queue; the tests run a thread of their own.
/// </summary>
public interface IUiScheduler
{
    /// <summary>True on the owning thread.</summary>
    bool IsOnUiThread { get; }

    /// <summary>Runs <paramref name="action"/> on the owning thread, and completes after it has run.</summary>
    Task RunAsync(Action action);
}
