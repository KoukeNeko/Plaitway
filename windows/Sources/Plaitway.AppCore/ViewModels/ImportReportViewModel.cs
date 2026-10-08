using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.Text;

namespace Plaitway.AppCore.ViewModels;

/// <summary>One file of an import: the file, what became of it, and the lines that explain it.</summary>
/// <param name="Filename">The file.</param>
/// <param name="ProfileName">The name of the profile it became; empty when it was refused.</param>
/// <param name="Details">What the daemon changed in it, or why it was refused, one line each.</param>
/// <param name="Icon">The shape of the outcome: stored, stored with changes, refused.</param>
/// <param name="Tone">The colour family of the outcome.</param>
/// <param name="AutomationName">What a screen reader says: the file, then how it went.</param>
public sealed record ImportRow(string Filename, string ProfileName, IReadOnlyList<string> Details, StatusIcon Icon, StatusTone Tone, string AutomationName);

/// <summary>What came of importing the files the user picked or dropped, for the dialog that says so.</summary>
public sealed class ImportReportViewModel
{
    /// <summary>Makes the dialog for <paramref name="report"/>.</summary>
    public ImportReportViewModel(ImportReport report, UiText text)
    {
        Text = text;
        Rows = [.. report.Outcomes.Select(outcome => RowOf(outcome, text))];
    }

    /// <summary>The strings.</summary>
    public UiText Text { get; }

    /// <summary>One row per file, in the order they were given.</summary>
    public IReadOnlyList<ImportRow> Rows { get; }

    private static ImportRow RowOf(ImportOutcome outcome, UiText text) => outcome switch
    {
        ImportOutcome.Imported { Warnings.Count: 0 } imported =>
            new ImportRow(imported.Filename, imported.Name, [], StatusIcon.RouteInstalled, StatusTone.Success, $"{imported.Filename}, {imported.Name}"),
        ImportOutcome.Imported imported => new ImportRow(
            imported.Filename,
            imported.Name,
            [.. imported.Warnings.Select(warning => warning.Summary(text))],
            StatusIcon.RouteBlocked,
            StatusTone.Caution,
            $"{imported.Filename}, {imported.Name}, {text.Attention}"),
        ImportOutcome.Failed failed => new ImportRow(failed.Filename, string.Empty, [failed.Message], StatusIcon.RouteFailed, StatusTone.Critical, $"{failed.Filename}, {text.Failed}"),
        _ => throw new ArgumentOutOfRangeException(nameof(outcome)),
    };
}
