using System.ComponentModel;
using System.Diagnostics;
using System.Globalization;
using Plaitway.Client.Tests.Support;

namespace Plaitway.App.UiTests.Support;

/// <summary>The app, started against a daemon of the test's own, and ended with the test.</summary>
internal sealed class AppProcess : IAsyncDisposable
{
    private const string AppDirectory = @"Sources\Plaitway.App\bin\Debug\net10.0-windows10.0.19041.0\win-x64";
    private const string ReportName = "report.txt";
    private const string TrayWindowKey = "tray_window=";
    private const string SettingsFileName = "settings.json";
    private const string SocketVariable = "PLAITWAY_SOCKET";
    private const string ReportVariable = "PLAITWAY_RUN_REPORT";
    private const string WindowBounds = "60,40,1180,780";

    // The messages of the notification-area icon: WM_APP + 1 is what it sends, with the position in the word parameter.
    private const uint TrayCallbackMessage = 0x8001;
    private const int WmContextMenu = 0x7B;
    private const int NinSelect = 0x400;
    private const int MenuX = 3000;
    private const int MenuY = 1500;
    private const int WordBits = 16;
    private const int WordMask = 0xFFFF;

    private static readonly TimeSpan WindowTimeout = TimeSpan.FromSeconds(30);

    private readonly TempDirectory _directory;
    private readonly bool _ownsDirectory;
    private readonly DateTime _startedAt = DateTime.Now;

    private AppProcess(Process process, TempDirectory directory, bool ownsDirectory = true)
    {
        Process = process;
        _directory = directory;
        _ownsDirectory = ownsDirectory;
    }

    /// <summary>The process.</summary>
    public Process Process { get; }

    /// <summary>The window, once it is on the screen.</summary>
    public nint WindowHandle { get; private set; }

    /// <summary>Starts the app on <paramref name="pipe"/> and waits for its window.</summary>
    /// <param name="pipe">The daemon's pipe.</param>
    /// <param name="language">The language the command line names; null leaves it to the language the app was told to keep.</param>
    /// <param name="savedLanguage">The language the app kept the last time it ran, as a person chose it in the settings; null for none.</param>
    public static async Task<AppProcess> StartAsync(string pipe, string? language = "en-US", string? savedLanguage = null)
    {
        var app = Launch(pipe, language, savedLanguage);
        try
        {
            await app.WaitForWindowAsync();
            return app;
        }
        catch
        {
            await app.DisposeAsync();
            throw;
        }
    }

    /// <summary>
    /// Starts another copy of the app, as a double-click on a shortcut would, and returns it without waiting. It is meant
    /// to find the first copy and end; the caller disposes it.
    /// </summary>
    public static AppProcess StartSecondCopy(string pipe) => Launch(pipe, "en-US", savedLanguage: null);

    /// <summary>
    /// The copy that this one started when it restarted itself, once its window is on the screen. It belongs to the same test:
    /// disposing it ends it, and the files of this one stay for as long as this one is not disposed.
    /// </summary>
    public async Task<AppProcess> WaitForRestartedCopyAsync()
    {
        var deadline = DateTime.UtcNow + WindowTimeout;
        while (DateTime.UtcNow < deadline)
        {
            if (FindRestartedCopy() is { } process)
            {
                var copy = new AppProcess(process, _directory, ownsDirectory: false);
                try
                {
                    await copy.WaitForWindowAsync();
                    return copy;
                }
                catch
                {
                    await copy.DisposeAsync();
                    throw;
                }
            }

            await Task.Delay(TimeSpan.FromMilliseconds(100));
        }

        throw new TimeoutException("the app did not start another copy of itself");
    }

    /// <summary>The icon asked for its menu, as a right click on it does.</summary>
    public void OpenTrayMenu()
    {
        var delivered = Native.PostMessage(TrayWindow(), TrayCallbackMessage, Position(MenuX, MenuY), WmContextMenu);
        Assert.True(delivered, "the icon's window did not take the message");
    }

    /// <summary>The icon was clicked, as a left click on it does.</summary>
    public void ClickTrayIcon()
    {
        var delivered = Native.PostMessage(TrayWindow(), TrayCallbackMessage, 0, NinSelect);
        Assert.True(delivered, "the icon's window did not take the message");
    }

    /// <inheritdoc />
    public ValueTask DisposeAsync()
    {
        if (!Process.HasExited)
        {
            Process.Kill(entireProcessTree: true);
            Process.WaitForExit();
        }

        Process.Dispose();
        if (_ownsDirectory)
        {
            _directory.Dispose();
        }

        return ValueTask.CompletedTask;
    }

    private static AppProcess Launch(string pipe, string? language, string? savedLanguage)
    {
        var directory = new TempDirectory();
        var dataDirectory = Path.Combine(directory.Path, "app");
        if (savedLanguage is not null)
        {
            // What the settings page keeps; a black-box test writes the file as the app does.
            Directory.CreateDirectory(dataDirectory);
            File.WriteAllText(Path.Combine(dataDirectory, SettingsFileName), "{\"Language\":\"" + savedLanguage + "\"}");
        }

        var start = new ProcessStartInfo(ExecutablePath()) { UseShellExecute = false };
        start.Environment[SocketVariable] = pipe;
        start.Environment[ReportVariable] = Path.Combine(directory.Path, ReportName);
        var arguments = new List<string> { "--bounds", WindowBounds, "--topmost", "--theme", "light", "--data-dir", dataDirectory };
        if (language is not null)
        {
            arguments.AddRange(["--language", language]);
        }

        foreach (var argument in arguments)
        {
            start.ArgumentList.Add(argument);
        }

        var process = Process.Start(start) ?? throw new InvalidOperationException("the app did not start");
        return new AppProcess(process, directory);
    }

    private Process? FindRestartedCopy()
    {
        var path = ExecutablePath();
        foreach (var candidate in Process.GetProcessesByName(Path.GetFileNameWithoutExtension(path)))
        {
            try
            {
                if (candidate.Id != Process.Id && candidate.StartTime > _startedAt && string.Equals(candidate.MainModule?.FileName, path, StringComparison.OrdinalIgnoreCase))
                {
                    return candidate;
                }
            }
            catch (Exception error) when (error is Win32Exception or InvalidOperationException)
            {
                // A process of another session, or one that has just ended: not the copy.
            }

            candidate.Dispose();
        }

        return null;
    }

    private static string ExecutablePath()
    {
        var path = Path.Combine(Repository.Root, "windows", AppDirectory, "Plaitway.exe");
        return File.Exists(path) ? path : throw new FileNotFoundException("the app is not built: dotnet build windows\\Sources\\Plaitway.App", path);
    }

    private static nuint Position(int x, int y) => (nuint)(uint)((y << WordBits) | (x & WordMask));

    private async Task WaitForWindowAsync()
    {
        var deadline = DateTime.UtcNow + WindowTimeout;
        while (DateTime.UtcNow < deadline)
        {
            Process.Refresh();
            if (Process.HasExited)
            {
                throw new InvalidOperationException($"the app ended with {Process.ExitCode} before it showed a window");
            }

            if (Process.MainWindowHandle != 0)
            {
                WindowHandle = Process.MainWindowHandle;
                Native.SetForegroundWindow(WindowHandle);
                return;
            }

            await Task.Delay(TimeSpan.FromMilliseconds(100));
        }

        throw new TimeoutException("the app did not show a window");
    }

    private nint TrayWindow()
    {
        var report = Path.Combine(_directory.Path, ReportName);
        var line = File.ReadLines(report).FirstOrDefault(entry => entry.StartsWith(TrayWindowKey, StringComparison.Ordinal))
            ?? throw new InvalidOperationException("the app has not said where its icon is");
        return (nint)long.Parse(line[TrayWindowKey.Length..], CultureInfo.InvariantCulture);
    }
}
