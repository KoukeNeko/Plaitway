using System.Runtime.InteropServices;

namespace Plaitway.App.Windowing;

/// <summary>
/// Keeps a window from being made smaller than the layout can bear. WinUI has no minimum size for a window, so the
/// window asks Windows (<c>WM_GETMINMAXINFO</c>) through a subclass of its own procedure, which goes with the window.
/// </summary>
internal sealed partial class WindowMinimumSize
{
    private const uint GetMinMaxInfoMessage = 0x0024;
    private const nuint SubclassId = 1;
    private const double DipsPerInch = 96.0;

    private readonly double _widthDips;
    private readonly double _heightDips;

    // The system holds this function pointer until the subclass is removed, so the delegate lives as long as the object.
    private readonly SubclassProcedure _procedure;
    private readonly nint _procedurePointer;

    public WindowMinimumSize(nint window, double widthDips, double heightDips)
    {
        _widthDips = widthDips;
        _heightDips = heightDips;
        _procedure = Handle;
        _procedurePointer = Marshal.GetFunctionPointerForDelegate(_procedure);
        SetWindowSubclass(window, _procedurePointer, SubclassId, 0);
    }

    private delegate nint SubclassProcedure(nint window, uint message, nuint wordParameter, nint longParameter, nuint subclassId, nuint data);

    private nint Handle(nint window, uint message, nuint wordParameter, nint longParameter, nuint subclassId, nuint data)
    {
        if (message == GetMinMaxInfoMessage)
        {
            var scale = Win32Window.DpiOf(window) / DipsPerInch;
            var info = Marshal.PtrToStructure<MinMaxInfo>(longParameter);
            info.MinTrackSize = new Point((int)(_widthDips * scale), (int)(_heightDips * scale));
            Marshal.StructureToPtr(info, longParameter, fDeleteOld: false);
            return 0;
        }

        return DefSubclassProc(window, message, wordParameter, longParameter);
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct Point(int x, int y)
    {
        public int X = x;
        public int Y = y;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct MinMaxInfo
    {
        public Point Reserved;
        public Point MaxSize;
        public Point MaxPosition;
        public Point MinTrackSize;
        public Point MaxTrackSize;
    }

    [LibraryImport("comctl32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static partial bool SetWindowSubclass(nint window, nint procedure, nuint subclassId, nuint data);

    [LibraryImport("comctl32.dll")]
    private static partial nint DefSubclassProc(nint window, uint message, nuint wordParameter, nint longParameter);
}
