namespace Plaitway.AppCore.Editing;

/// <summary>
/// The line endings of a profile's text. A text box counts a line break as one character whichever the file used, so
/// the editor works on "\n" alone and the file's own ending is put back when the text is stored.
/// </summary>
public static class LineEndings
{
    private const string CarriageReturnLineFeed = "\r\n";
    private const string LineFeed = "\n";
    private const char CarriageReturn = '\r';

    /// <summary>Every line break of <paramref name="text"/> as a line feed.</summary>
    public static string Normalize(string text) =>
        text.Contains(CarriageReturn, StringComparison.Ordinal)
            ? text.Replace(CarriageReturnLineFeed, LineFeed, StringComparison.Ordinal).Replace(CarriageReturn, '\n')
            : text;

    /// <summary>Whether the file ends its lines with a carriage return and a line feed, as a file made on Windows does.</summary>
    public static bool UsesCarriageReturn(string text) => text.Contains(CarriageReturnLineFeed, StringComparison.Ordinal);

    /// <summary>Gives a normalized <paramref name="text"/> the ending the file had.</summary>
    public static string Restore(string text, bool usesCarriageReturn) =>
        usesCarriageReturn ? text.Replace(LineFeed, CarriageReturnLineFeed, StringComparison.Ordinal) : text;
}
