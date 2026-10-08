using System.Diagnostics;
using System.Text;
using Plaitway.Client.Transport;

namespace Plaitway.Client.Tests.Support;

/// <summary>The daemon could not be started; <see cref="Exception.Message"/> says why, with what it printed.</summary>
public sealed class DaemonStartException(string message) : Exception(message);

/// <summary>
/// Runs the real Go daemon on its in-memory backend (<c>-fake</c>), with its own pipe and an empty state
/// directory, so that every test starts from no profiles. Without <c>fake</c> it runs the real engines.
/// </summary>
public sealed class DaemonProcess : IAsyncDisposable
{
    private const string ListeningMarker = "listening";
    private static readonly TimeSpan StartTimeout = TimeSpan.FromSeconds(15);

    private readonly DaemonBinary _binary;
    private readonly bool _fake;
    private readonly string _directory;
    private readonly StringBuilder _log = new();
    private readonly object _logLock = new();
    private Process? _process;

    private DaemonProcess(DaemonBinary binary, bool fake)
    {
        _binary = binary;
        _fake = fake;
        _directory = Path.Combine(Path.GetTempPath(), "pw-daemon-" + Guid.NewGuid().ToString("N")[..8]);
        PipeName = $"{PipePath.Namespace}plaitway-cs-test-{Environment.ProcessId}-{Guid.NewGuid().ToString("N")[..12]}";
        Directory.CreateDirectory(_directory);
    }

    /// <summary>The full name of the pipe the daemon listens on; machine-wide, so unique to this daemon.</summary>
    public string PipeName { get; }

    /// <summary>The pipe as the client takes it.</summary>
    public PipePath Pipe => PipePath.Parse(PipeName);

    /// <summary>Everything the daemon has logged so far.</summary>
    public string Log
    {
        get
        {
            lock (_logLock)
            {
                return _log.ToString();
            }
        }
    }

    /// <summary>Starts a daemon and waits until it listens.</summary>
    /// <exception cref="DaemonStartException">The daemon exits or does not listen in time.</exception>
    public static async Task<DaemonProcess> StartAsync(DaemonBinary binary, bool fake = true)
    {
        var daemon = new DaemonProcess(binary, fake);
        try
        {
            await daemon.StartAsync().ConfigureAwait(false);
            return daemon;
        }
        catch
        {
            await daemon.DisposeAsync().ConfigureAwait(false);
            throw;
        }
    }

    /// <summary>Starts the daemon again after <see cref="Kill"/>, on the same pipe and with the same stored profiles.</summary>
    public async Task StartAsync()
    {
        var start = new ProcessStartInfo(_binary.Path)
        {
            RedirectStandardError = true,
            RedirectStandardOutput = true,
            UseShellExecute = false,
            CreateNoWindow = true,
        };
        if (_fake)
        {
            start.ArgumentList.Add("-fake");
        }

        foreach (var argument in new[]
        {
            "-socket", PipeName,
            "-state-dir", Path.Combine(_directory, "state"),
            "-log-file", string.Empty,
            "-run-dir", Path.Combine(_directory, "run"),
            "-log-level", "debug",
        })
        {
            start.ArgumentList.Add(argument);
        }

        var listening = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        var process = Process.Start(start) ?? throw new DaemonStartException("the daemon did not start");
        process.ErrorDataReceived += (_, line) => Record(line.Data, listening);
        process.OutputDataReceived += (_, line) => Record(line.Data, listening);
        process.BeginErrorReadLine();
        process.BeginOutputReadLine();
        _process = process;
        _binary.Job.Add(process);

        var exited = process.WaitForExitAsync();
        var winner = await Task.WhenAny(listening.Task, exited, Task.Delay(StartTimeout)).ConfigureAwait(false);
        if (winner != listening.Task)
        {
            throw new DaemonStartException(
                (winner == exited ? $"the daemon exited with {process.ExitCode}" : "the daemon did not listen in time") + $":{Environment.NewLine}{Log}");
        }
    }

    /// <summary>Ends the daemon the way a crash does; its pipe goes with it.</summary>
    public void Kill()
    {
        if (_process is { HasExited: false } running)
        {
            running.Kill(entireProcessTree: true);
            running.WaitForExit();
        }
    }

    /// <inheritdoc />
    public ValueTask DisposeAsync()
    {
        Kill();
        _process?.Dispose();
        try
        {
            Directory.Delete(_directory, recursive: true);
        }
        catch (IOException error)
        {
            Trace.TraceWarning($"could not remove {_directory}: {error.Message}");
        }

        return ValueTask.CompletedTask;
    }

    private void Record(string? line, TaskCompletionSource listening)
    {
        if (line is null)
        {
            return;
        }

        lock (_logLock)
        {
            _log.AppendLine(line);
        }

        if (line.Contains(ListeningMarker, StringComparison.Ordinal))
        {
            listening.TrySetResult();
        }
    }
}
