using System.Globalization;
using Microsoft.Windows.ApplicationModel.Resources;
using Plaitway.AppCore.Text;

namespace Plaitway.App.Platform;

/// <summary>
/// The strings of the app from the .resw files that WinUI loads (generated from the catalogs by
/// <c>Tools\Localization.targets</c>). They follow the language of Windows, or the one named; the resource manager picks
/// the closest language that exists, English when none does.
/// </summary>
internal sealed class MrtLocalizer : ILocalizer
{
    private const string ResourceMapName = "Resources";
    private const string LanguageQualifier = "Language";

    private readonly ResourceMap _map;
    private readonly ResourceContext _context;

    /// <param name="languageOverride">A language tag such as <c>zh-TW</c>; null to follow the system.</param>
    public MrtLocalizer(string? languageOverride)
    {
        var manager = new ResourceManager();
        _map = manager.MainResourceMap.GetSubtree(ResourceMapName);
        _context = manager.CreateResourceContext();
        if (languageOverride is not null)
        {
            _context.QualifierValues[LanguageQualifier] = languageOverride;
        }

        Culture = languageOverride is null ? CultureInfo.CurrentUICulture : CultureInfo.GetCultureInfo(languageOverride);
    }

    /// <inheritdoc />
    public CultureInfo Culture { get; }

    /// <inheritdoc />
    public string Resolve(string id) => _map.GetValue(id, _context).ValueAsString;
}
