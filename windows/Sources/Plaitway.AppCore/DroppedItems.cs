using Plaitway.AppCore.Text;

namespace Plaitway.AppCore;

/// <summary>One thing the user dropped on the window.</summary>
/// <param name="Name">What Explorer calls it.</param>
/// <param name="Path">Where the file is; null or empty for an item that is not a file on this PC (inside an archive, or from a browser).</param>
public sealed record DroppedItem(string Name, string? Path);

/// <summary>What a drop comes to: the files to import and the items that were left out, worded as import failures.</summary>
/// <param name="Files">The paths to import, each once.</param>
/// <param name="Ignored">One failure per item that is not a file on this PC, for the import report.</param>
public sealed record DropBatch(IReadOnlyList<string> Files, IReadOnlyList<ImportOutcome> Ignored)
{
    /// <summary>
    /// Sorts what was dropped. No file is judged by its name: the importer, like the file dialog and the drop on macOS,
    /// takes any file and lets the daemon tell a profile from something else by its content. A folder is passed on and
    /// the importer says it is not a text file.
    /// </summary>
    public static DropBatch Of(IEnumerable<DroppedItem> items, UiText text)
    {
        List<string> files = [];
        List<ImportOutcome> ignored = [];
        HashSet<string> seen = new(StringComparer.OrdinalIgnoreCase);
        foreach (var item in items)
        {
            if (string.IsNullOrWhiteSpace(item.Path))
            {
                ignored.Add(new ImportOutcome.Failed(item.Name, text.NotAFileOnThisPC));
            }
            else if (seen.Add(item.Path))
            {
                files.Add(item.Path);
            }
        }

        return new DropBatch(files, ignored);
    }
}
