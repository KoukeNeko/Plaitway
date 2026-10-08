using System.Text.Json;
using Microsoft.UI.Windowing;
using Windows.Graphics;

namespace Plaitway.App.Windowing;

/// <summary>Where the window was, for the next start.</summary>
/// <param name="X">Left edge in pixels of the virtual screen.</param>
/// <param name="Y">Top edge in pixels of the virtual screen.</param>
/// <param name="WidthDips">Width in device-independent pixels (96 to the inch), so that it fits a monitor of another scale.</param>
/// <param name="HeightDips">Height in device-independent pixels.</param>
/// <param name="IsMaximized">The window was maximized.</param>
internal sealed record SavedPlacement(int X, int Y, double WidthDips, double HeightDips, bool IsMaximized);

/// <summary>Keeps the placement of the window in a small file of the user's.</summary>
/// <param name="path">The file.</param>
internal sealed class WindowPlacementStore(string path)
{
    /// <summary>The placement last saved; null when there is none or the file is not one of ours.</summary>
    public SavedPlacement? Load()
    {
        try
        {
            return JsonSerializer.Deserialize<SavedPlacement>(File.ReadAllText(path));
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or JsonException)
        {
            // A first start, or a file that was damaged: the window opens where Windows puts it.
            return null;
        }
    }

    /// <summary>Saves the placement; a folder that cannot be written to loses it, which the user notices as a window that opens in the middle.</summary>
    public void Save(SavedPlacement placement)
    {
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            File.WriteAllText(path, JsonSerializer.Serialize(placement));
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            System.Diagnostics.Trace.TraceWarning($"could not save the window placement: {error.Message}");
        }
    }
}

/// <summary>Puts the window where it was, on the monitor it was on, and remembers where it goes.</summary>
internal sealed class WindowPlacement
{
    private const double DefaultWidthDips = 1040;
    private const double DefaultHeightDips = 660;
    private const double DipsPerInch = 96.0;
    private const int SaveDelayMilliseconds = 600;

    private readonly AppWindow _window;
    private readonly nint _handle;
    private readonly WindowPlacementStore _store;
    private readonly Microsoft.UI.Dispatching.DispatcherQueueTimer _saveTimer;
    private SavedPlacement? _normal;

    public WindowPlacement(AppWindow window, nint handle, WindowPlacementStore store, Microsoft.UI.Dispatching.DispatcherQueue queue)
    {
        _window = window;
        _handle = handle;
        _store = store;
        _saveTimer = queue.CreateTimer();
        _saveTimer.Interval = TimeSpan.FromMilliseconds(SaveDelayMilliseconds);
        _saveTimer.IsRepeating = false;
        _saveTimer.Tick += (_, _) => Save();
    }

    /// <summary>Where the window should open: the saved place when a monitor still shows it, otherwise the middle of the main one.</summary>
    public void Restore(RectInt32? requested)
    {
        if (requested is { } bounds)
        {
            _window.MoveAndResize(bounds);
            return;
        }

        var saved = _store.Load();
        if (saved is not null && PlaceOnItsMonitor(saved))
        {
            return;
        }

        PlaceInTheMiddle();
    }

    /// <summary>Starts remembering; the window's moves and size changes are saved a moment after the last.</summary>
    public void Track()
    {
        Capture();
        _window.Changed += (_, args) =>
        {
            if (args.DidPositionChange || args.DidSizeChange || args.DidPresenterChange)
            {
                Capture();
                _saveTimer.Stop();
                _saveTimer.Start();
            }
        };
    }

    /// <summary>Saves now, as the window is hidden or the app ends.</summary>
    public void Save()
    {
        _saveTimer.Stop();
        Capture();
        if (_normal is not null)
        {
            _store.Save(_normal);
        }
    }

    /// <summary>The window's own bounds while it is neither maximized nor minimized; those are what a restore needs.</summary>
    private void Capture()
    {
        if (_window.Presenter is not OverlappedPresenter presenter || presenter.State == OverlappedPresenterState.Minimized)
        {
            return;
        }

        var dpi = Win32Window.DpiOf(_handle);
        if (presenter.State == OverlappedPresenterState.Maximized)
        {
            _normal = _normal is null ? null : _normal with { IsMaximized = true };
            return;
        }

        _normal = new SavedPlacement(
            _window.Position.X,
            _window.Position.Y,
            _window.Size.Width * DipsPerInch / dpi,
            _window.Size.Height * DipsPerInch / dpi,
            IsMaximized: false);
    }

    private bool PlaceOnItsMonitor(SavedPlacement saved)
    {
        var dpi = Win32Window.DpiAt(saved.X, saved.Y);
        var size = new SizeInt32((int)(saved.WidthDips * dpi / DipsPerInch), (int)(saved.HeightDips * dpi / DipsPerInch));
        var bounds = new RectInt32(saved.X, saved.Y, size.Width, size.Height);

        // A monitor that was unplugged since leaves the window nowhere to be seen.
        if (DisplayArea.GetFromRect(bounds, DisplayAreaFallback.None) is null)
        {
            return false;
        }

        _window.MoveAndResize(bounds);

        // What a restore of a maximized window goes back to.
        _normal = saved;
        if (saved.IsMaximized && _window.Presenter is OverlappedPresenter presenter)
        {
            presenter.Maximize();
        }

        return true;
    }

    private void PlaceInTheMiddle()
    {
        var area = DisplayArea.Primary.WorkArea;
        var dpi = Win32Window.DpiAt(area.X, area.Y);
        var width = Math.Min((int)(DefaultWidthDips * dpi / DipsPerInch), area.Width);
        var height = Math.Min((int)(DefaultHeightDips * dpi / DipsPerInch), area.Height);
        _window.MoveAndResize(new RectInt32(area.X + ((area.Width - width) / 2), area.Y + ((area.Height - height) / 2), width, height));
    }
}
