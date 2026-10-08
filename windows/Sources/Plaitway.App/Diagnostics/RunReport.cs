using System.Globalization;
using System.Text;

namespace Plaitway.App.Diagnostics;

/// <summary>
/// Writes what a scripted run needs to know (whether the notification area accepted the icon, how often it was put back,
/// when the window was first shown) as lines of <c>name=value</c> to the file named by <c>PLAITWAY_RUN_REPORT</c>. Without
/// the variable it does nothing.
/// </summary>
internal sealed class RunReport
{
    private const string PathVariable = "PLAITWAY_RUN_REPORT";

    private readonly string? _path = Environment.GetEnvironmentVariable(PathVariable);
    private readonly object _lock = new();

    public void Write(string name, object? value)
    {
        if (string.IsNullOrEmpty(_path))
        {
            return;
        }

        var line = string.Create(CultureInfo.InvariantCulture, $"{name}={value}{Environment.NewLine}");
        lock (_lock)
        {
            File.AppendAllText(_path, line, Encoding.UTF8);
        }
    }
}
