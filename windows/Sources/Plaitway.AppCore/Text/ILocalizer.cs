using System.Globalization;

namespace Plaitway.AppCore.Text;

/// <summary>
/// Where the text of a language comes from. The app reads the resources WinUI loads; the tests read the same
/// strings from the catalogs. Nothing outside <see cref="UiText"/> asks for a string by its id.
/// </summary>
public interface ILocalizer
{
    /// <summary>The culture the strings are in, for the numbers and dates that are put into them.</summary>
    CultureInfo Culture { get; }

    /// <summary>The string with the resource id <paramref name="id"/>, as a .NET composite format when it takes arguments.</summary>
    string Resolve(string id);
}
