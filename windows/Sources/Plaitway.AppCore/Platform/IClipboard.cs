namespace Plaitway.AppCore.Platform;

/// <summary>The clipboard, which the view layer owns.</summary>
public interface IClipboard
{
    /// <summary>Replaces the clipboard's content with <paramref name="text"/>.</summary>
    void SetText(string text);
}

/// <summary>The dialog that asks the user for profile files.</summary>
public interface IFilePicker
{
    /// <summary>The paths the user chose; empty when the dialog was cancelled. Any file can be chosen: the daemon tells an OpenVPN profile from a WireGuard one by its content.</summary>
    Task<IReadOnlyList<string>> PickProfileFilesAsync();
}
