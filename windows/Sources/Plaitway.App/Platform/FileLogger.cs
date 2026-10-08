using System.Globalization;
using System.Text;
using Microsoft.Extensions.Logging;

namespace Plaitway.App.Platform;

/// <summary>
/// Writes the app's log to one file, <c>%LocalAppData%\Plaitway\Logs\app.log</c>, and starts it over when it grows past a
/// limit, keeping the last one as <c>app.1.log</c>. What is logged is what <see cref="Plaitway.AppCore.LogMessages"/> takes:
/// never a credential, a key or the text of a profile.
/// </summary>
internal sealed class FileLoggerProvider : ILoggerProvider
{
    private const long MaxBytes = 1_000_000;
    private const string TimeFormat = "yyyy-MM-dd HH:mm:ss.fff";

    private readonly string _path;
    private readonly object _lock = new();

    /// <param name="path">The log file; its folder is made.</param>
    public FileLoggerProvider(string path)
    {
        _path = path;
        Directory.CreateDirectory(Path.GetDirectoryName(path)!);
    }

    /// <inheritdoc />
    public ILogger CreateLogger(string categoryName) => new FileLogger(this, categoryName);

    /// <summary>A logger for the class <typeparamref name="T"/>, which is how the app's own classes take theirs.</summary>
    public ILogger<T> CreateLogger<T>() => new TypedLogger<T>(CreateLogger(typeof(T).FullName ?? typeof(T).Name));

    /// <inheritdoc />
    public void Dispose()
    {
    }

    private void Write(string line)
    {
        lock (_lock)
        {
            try
            {
                RollOverWhenFull();
                File.AppendAllText(_path, line + Environment.NewLine, Encoding.UTF8);
            }
            catch (IOException)
            {
                // A log that cannot be written must not stop the app; the next line tries again.
            }
            catch (UnauthorizedAccessException)
            {
                // As above.
            }
        }
    }

    private void RollOverWhenFull()
    {
        if (File.Exists(_path) && new FileInfo(_path).Length > MaxBytes)
        {
            File.Move(_path, Path.ChangeExtension(_path, ".1.log"), overwrite: true);
        }
    }

    private sealed class FileLogger(FileLoggerProvider owner, string category) : ILogger
    {
        public IDisposable? BeginScope<TState>(TState state)
            where TState : notnull => null;

        public bool IsEnabled(LogLevel logLevel) => logLevel != LogLevel.None;

        public void Log<TState>(LogLevel logLevel, EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter)
        {
            var time = DateTime.Now.ToString(TimeFormat, CultureInfo.InvariantCulture);
            var line = $"{time} {logLevel} {category}: {formatter(state, exception)}";
            owner.Write(exception is null ? line : $"{line}{Environment.NewLine}{exception}");
        }
    }

    private sealed class TypedLogger<T>(ILogger inner) : ILogger<T>
    {
        public IDisposable? BeginScope<TState>(TState state)
            where TState : notnull => inner.BeginScope(state);

        public bool IsEnabled(LogLevel logLevel) => inner.IsEnabled(logLevel);

        public void Log<TState>(LogLevel logLevel, EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter) =>
            inner.Log(logLevel, eventId, state, exception, formatter);
    }
}
