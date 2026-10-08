using Windows.Graphics;

namespace Plaitway.App.Startup;

/// <summary>
/// What the command line and the environment can say. Most of it exists for scripted runs and screenshots (a window of known
/// size and place, a language and a theme that the system is not set to, a folder of its own for what the app keeps); an
/// ordinary start passes none.
/// </summary>
/// <param name="Theme">"light" or "dark" to draw the window in a theme other than the system's; null to follow the system.</param>
/// <param name="Language">A language tag such as <c>zh-TW</c> to load the strings of, instead of the system's; null to follow the system.</param>
/// <param name="Bounds">Where to put the window and how large to make it, in pixels; null to leave it to the remembered place.</param>
/// <param name="AlwaysOnTop">Keeps the window above the others, so that a scripted run can rely on where it is.</param>
/// <param name="StartHidden">Starts in the notification area without showing the window.</param>
/// <param name="DataDirectory">Where the app keeps its window placement and its log; null for <c>%LocalAppData%\Plaitway</c>.</param>
internal sealed record AppOptions(string? Theme, string? Language, RectInt32? Bounds, bool AlwaysOnTop, bool StartHidden, string? DataDirectory)
{
    /// <summary>The environment variable that names the language, for a run that cannot take a switch.</summary>
    public const string LanguageVariable = "PLAITWAY_LANGUAGE";

    private const string ThemeSwitch = "--theme";
    private const string LanguageSwitch = "--language";
    private const string BoundsSwitch = "--bounds";
    private const string AlwaysOnTopSwitch = "--topmost";
    private const string StartHiddenSwitch = "--hidden";
    private const string DataDirectorySwitch = "--data-dir";
    private const int BoundsFieldCount = 4;

    /// <summary>Reads the arguments after the program's own name; what it does not know is ignored.</summary>
    public static AppOptions Parse(IReadOnlyList<string> arguments, Func<string, string?> environment)
    {
        string? theme = null;
        string? language = environment(LanguageVariable) is { Length: > 0 } fromEnvironment ? fromEnvironment : null;
        string? dataDirectory = null;
        RectInt32? bounds = null;
        var alwaysOnTop = false;
        var startHidden = false;
        for (var index = 0; index < arguments.Count; index++)
        {
            switch (arguments[index])
            {
                case ThemeSwitch when index + 1 < arguments.Count:
                    theme = arguments[++index];
                    break;
                case LanguageSwitch when index + 1 < arguments.Count:
                    language = arguments[++index];
                    break;
                case BoundsSwitch when index + 1 < arguments.Count:
                    bounds = ParseBounds(arguments[++index]);
                    break;
                case DataDirectorySwitch when index + 1 < arguments.Count:
                    dataDirectory = arguments[++index];
                    break;
                case AlwaysOnTopSwitch:
                    alwaysOnTop = true;
                    break;
                case StartHiddenSwitch:
                    startHidden = true;
                    break;
                default:
                    break;
            }
        }

        return new AppOptions(theme, language, bounds, alwaysOnTop, startHidden, dataDirectory);
    }

    private static RectInt32? ParseBounds(string text)
    {
        var fields = text.Split(',');
        if (fields.Length != BoundsFieldCount || !fields.All(field => int.TryParse(field, out _)))
        {
            return null;
        }

        var numbers = fields.Select(int.Parse).ToArray();
        return new RectInt32(numbers[0], numbers[1], numbers[2], numbers[3]);
    }
}
