using Microsoft.Win32;
using Plaitway.AppCore.Helper;
using Plaitway.AppCore.Platform;
using Windows.ApplicationModel.DataTransfer;
using Windows.Storage.Pickers;
using WinRT.Interop;

namespace Plaitway.App.Platform;

/// <summary><see cref="IClipboard"/> on the Windows clipboard; the text stays after the app ends.</summary>
internal sealed class WindowsClipboard : IClipboard
{
    /// <inheritdoc />
    public void SetText(string text)
    {
        var package = new DataPackage();
        package.SetText(text);
        Clipboard.SetContent(package);
        Clipboard.Flush();
    }
}

/// <summary><see cref="IFilePicker"/> on the system's file dialog, owned by the main window.</summary>
/// <param name="windowHandle">The handle of the window the dialog belongs to.</param>
internal sealed class WindowsFilePicker(Func<nint> windowHandle) : IFilePicker
{
    private const string AnyFile = "*";

    /// <inheritdoc />
    public async Task<IReadOnlyList<string>> PickProfileFilesAsync()
    {
        var picker = new FileOpenPicker { SuggestedStartLocation = PickerLocationId.Downloads };

        // Any file can be chosen: the daemon tells an OpenVPN profile from a WireGuard one by its content, and a filter
        // by extension would leave out the profiles that other tools named differently.
        picker.FileTypeFilter.Add(AnyFile);
        InitializeWithWindow.Initialize(picker, windowHandle());
        var files = await picker.PickMultipleFilesAsync();
        return [.. files.Select(file => file.Path)];
    }
}

/// <summary>
/// The startup list of the user (<c>HKCU\...\Run</c>): an entry that starts the app, hidden in the notification area,
/// when the user signs in. A debug build cannot add itself: it is not where it will stay.
/// </summary>
internal sealed class RunKeyStartup(string executablePath, bool isAvailable) : IStartupRegistration
{
    private const string RunKeyPath = @"Software\Microsoft\Windows\CurrentVersion\Run";
    private const string ValueName = "Plaitway";
    private const string HiddenSwitch = "--hidden";

    /// <inheritdoc />
    public bool IsAvailable => isAvailable;

    /// <inheritdoc />
    public bool IsEnabled
    {
        get
        {
            using var key = Registry.CurrentUser.OpenSubKey(RunKeyPath);
            return key?.GetValue(ValueName) is string;
        }
    }

    /// <inheritdoc />
    public void SetEnabled(bool enabled)
    {
        if (!isAvailable)
        {
            throw new InvalidOperationException("this copy of the app cannot add itself to the startup list");
        }

        try
        {
            using var key = Registry.CurrentUser.CreateSubKey(RunKeyPath);
            if (enabled)
            {
                key.SetValue(ValueName, $"\"{executablePath}\" {HiddenSwitch}");
            }
            else
            {
                key.DeleteValue(ValueName, throwOnMissingValue: false);
            }
        }
        catch (Exception error) when (error is UnauthorizedAccessException or System.Security.SecurityException or IOException)
        {
            throw new InvalidOperationException(error.Message, error);
        }
    }
}

/// <summary>
/// The launcher of a copy of the app that does not manage the helper (it talks to a developer's daemon): it refuses, so
/// that nothing in such a copy can show the user a consent prompt for the helper.
/// </summary>
internal sealed class DisabledElevatedLauncher : IElevatedLauncher
{
    /// <inheritdoc />
    public Task<ElevatedResult> RunAsync(string executablePath, string arguments, CancellationToken cancellationToken = default) =>
        throw new InvalidOperationException("this copy of the app does not manage the helper");
}
