using System.Text.Json;

namespace Plaitway.AppCore.Platform;

/// <summary>Keeps the language the person chose in a small file of theirs.</summary>
/// <param name="path">The file.</param>
public sealed class FileLanguagePreference(string path) : ILanguagePreference
{
    /// <summary>What the file holds.</summary>
    /// <param name="Language">The tag chosen; null for the language of Windows.</param>
    private sealed record Saved(string? Language);

    /// <inheritdoc />
    public string? Language
    {
        get
        {
            try
            {
                return JsonSerializer.Deserialize<Saved>(File.ReadAllText(path))?.Language;
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException)
            {
                // A first start, or a file that was damaged: the app follows Windows.
                return null;
            }
        }
    }

    /// <inheritdoc />
    public void Save(string? language)
    {
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            File.WriteAllText(path, JsonSerializer.Serialize(new Saved(language)));
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            throw new InvalidOperationException(error.Message, error);
        }
    }
}
