using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;

namespace Plaitway.Client.Tests.Support;

/// <summary>
/// The real Go daemon the integration tests run: <c>PLAITWAY_DAEMON</c> names a binary, otherwise it is
/// built from this checkout, once per test run, next to the repository's other build output.
/// </summary>
public sealed class DaemonBinary : IAsyncLifetime
{
    private const string BinaryVariable = "PLAITWAY_DAEMON";
    private const string BuildDirectoryPrefix = "clienttests-";
    private static readonly TimeSpan BuildTimeout = TimeSpan.FromMinutes(5);

    private string? _buildDirectory;

    /// <summary>Ends the daemons of a test run that did not clean up after itself.</summary>
    internal KillOnCloseJob Job { get; } = new();

    /// <summary>The daemon executable.</summary>
    public string Path { get; private set; } = string.Empty;

    /// <inheritdoc />
    public async ValueTask InitializeAsync()
    {
        var named = Environment.GetEnvironmentVariable(BinaryVariable);
        if (!string.IsNullOrEmpty(named))
        {
            Path = named;
            return;
        }

        _buildDirectory = System.IO.Path.Combine(Repository.Root, "bin", BuildDirectoryPrefix + Environment.ProcessId);
        Path = System.IO.Path.Combine(_buildDirectory, "plaitwayd.exe");
        await BuildAsync().ConfigureAwait(false);
    }

    /// <inheritdoc />
    public ValueTask DisposeAsync()
    {
        Job.Dispose();
        if (_buildDirectory is not null && Directory.Exists(_buildDirectory))
        {
            try
            {
                Directory.Delete(_buildDirectory, recursive: true);
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException)
            {
                Trace.TraceWarning($"could not remove {_buildDirectory}: {error.Message}");
            }
        }

        return ValueTask.CompletedTask;
    }

    private async Task BuildAsync()
    {
        var start = new ProcessStartInfo("go")
        {
            WorkingDirectory = Repository.Root,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
            CreateNoWindow = true,
        };
        start.ArgumentList.Add("build");
        start.ArgumentList.Add("-o");
        start.ArgumentList.Add(Path);
        start.ArgumentList.Add("./cmd/plaitwayd");
        // A shell that was left set up for another platform must not decide what the tests run.
        start.Environment["GOOS"] = "windows";
        start.Environment["GOARCH"] = RuntimeInformation.ProcessArchitecture == Architecture.Arm64 ? "arm64" : "amd64";

        using var process = Process.Start(start) ?? throw new InvalidOperationException("go did not start");
        var output = new StringBuilder();
        process.OutputDataReceived += (_, line) => output.AppendLine(line.Data);
        process.ErrorDataReceived += (_, line) => output.AppendLine(line.Data);
        process.BeginOutputReadLine();
        process.BeginErrorReadLine();
        using var timeout = new CancellationTokenSource(BuildTimeout);
        await process.WaitForExitAsync(timeout.Token).ConfigureAwait(false);
        if (process.ExitCode != 0)
        {
            throw new InvalidOperationException($"go build failed with {process.ExitCode}:{Environment.NewLine}{output}");
        }
    }
}
