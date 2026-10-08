using System.Runtime.InteropServices;
using System.Security.Cryptography;
using System.Text;

namespace Plaitway.App.Startup;

/// <summary>
/// One copy of the app per user and daemon: a second start does not open a second window and a second icon, it asks the
/// first copy to come forward and ends. The first copy holds a named mutex; the second sets a named event that the first
/// waits on. Both names are in the user's session, so that two users on one machine each have their own copy.
/// </summary>
/// <remarks>
/// A copy that talks to a developer's daemon (<c>PLAITWAY_SOCKET</c>) is another instance than the installed one: its key
/// names the pipe, so that a test run does not hand its window to the app the user works with, and two runs against two
/// daemons do not hand theirs to each other.
/// </remarks>
internal sealed partial class SingleInstance : IDisposable
{
    private const string MutexPrefix = @"Local\Plaitway.Instance.";
    private const string EventPrefix = @"Local\Plaitway.Activate.";
    private const uint AllowAnyProcess = 0xFFFFFFFF;
    private const int KeyHashBytes = 6;

    private readonly Mutex? _mutex;
    private readonly EventWaitHandle _activation;
    private readonly CancellationTokenSource _stop = new();

    private SingleInstance(string key, Mutex? mutex, bool isPrimary)
    {
        _mutex = mutex;
        IsPrimary = isPrimary;
        _activation = new EventWaitHandle(initialState: false, EventResetMode.AutoReset, EventPrefix + key);
    }

    /// <summary>This copy is the first: it owns the window.</summary>
    public bool IsPrimary { get; }

    /// <summary>A second copy asked this one to come forward. Raised on a thread of the pool.</summary>
    public event Action? ActivationRequested;

    /// <summary>The key of the copies that share a window: the product, and the daemon when it is a developer's.</summary>
    public static string KeyFor(string? overriddenPipe) =>
        overriddenPipe is null
            ? "Product"
            : "Pipe-" + Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(overriddenPipe.ToUpperInvariant())).AsSpan(0, KeyHashBytes));

    /// <summary>Becomes the first copy, or finds that there is one.</summary>
    public static SingleInstance Acquire(string key)
    {
        var mutex = new Mutex(initiallyOwned: true, MutexPrefix + key, out var createdNew);
        if (createdNew)
        {
            return new SingleInstance(key, mutex, isPrimary: true);
        }

        mutex.Dispose();
        return new SingleInstance(key, mutex: null, isPrimary: false);
    }

    /// <summary>Starts waiting for second copies. Only the first copy does.</summary>
    public void Listen()
    {
        _ = Task.Run(() => WaitForActivations(_stop.Token));
    }

    /// <summary>Asks the first copy to come forward. This process was started by the user, so it may give the first the right to take the foreground.</summary>
    public void SignalPrimary()
    {
        AllowSetForegroundWindow(AllowAnyProcess);
        _activation.Set();
    }

    /// <inheritdoc />
    public void Dispose()
    {
        _stop.Cancel();
        _activation.Set();
        _activation.Dispose();
        if (IsPrimary)
        {
            _mutex?.ReleaseMutex();
        }

        _mutex?.Dispose();
        _stop.Dispose();
    }

    private void WaitForActivations(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                _activation.WaitOne();
            }
            catch (ObjectDisposedException)
            {
                return;
            }

            if (!cancellationToken.IsCancellationRequested)
            {
                ActivationRequested?.Invoke();
            }
        }
    }

    [LibraryImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool AllowSetForegroundWindow(uint processId);
}
