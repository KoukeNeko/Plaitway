using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Plaitway.AppCore.ViewModels;
using Windows.System;

namespace Plaitway.App.Views;

/// <summary>The content of the window: the sidebar and the page the sidebar selects, or the setup page.</summary>
internal sealed partial class ShellView : UserControl
{
    private const int SectionShortcutCount = 5;

    public ShellView(ShellViewModel viewModel)
    {
        ViewModel = viewModel;
        InitializeComponent();
        AddShortcuts();
        ViewModel.PropertyChanged += (_, args) =>
        {
            if (args.PropertyName == nameof(ShellViewModel.Page))
            {
                ShowPage();
            }
        };
        ShowPage();
    }

    public ShellViewModel ViewModel { get; }

    /// <summary>
    /// Puts the page where the sidebar's selection says. The content is emptied first, so that every page gets a view of its own
    /// rather than the view of the page before it, which would keep showing what that page showed.
    /// </summary>
    private void ShowPage()
    {
        PageHost.Content = null;
        PageHost.Content = ViewModel.Page;
    }

    /// <summary>
    /// The shortcuts of the window, which are what the menu bar of the macOS app is for: connect, disconnect all, import,
    /// find, the pages, and F6 to go from one part of the window to the next.
    /// </summary>
    private void AddShortcuts()
    {
        Add(VirtualKey.K, VirtualKeyModifiers.Control, () => _ = ViewModel.ToggleSelectedProfileAsync());
        Add(VirtualKey.K, VirtualKeyModifiers.Control | VirtualKeyModifiers.Shift, () => ViewModel.DisconnectAllCommand.Execute(null));
        Add(VirtualKey.I, VirtualKeyModifiers.Control, () => ViewModel.ImportCommand.Execute(null));
        Add(VirtualKey.F, VirtualKeyModifiers.Control, ViewModel.FindInPage);
        Add(VirtualKey.D, VirtualKeyModifiers.Control | VirtualKeyModifiers.Shift, ViewModel.ShowDiagnostics);
        Add((VirtualKey)0xBC, VirtualKeyModifiers.Control, ViewModel.ShowSettings);
        Add(VirtualKey.F6, VirtualKeyModifiers.None, () => MoveToNextPart(forward: true));
        Add(VirtualKey.F6, VirtualKeyModifiers.Shift, () => MoveToNextPart(forward: false));
        for (var number = 1; number <= SectionShortcutCount; number++)
        {
            var index = number - 1;
            Add((VirtualKey)((int)VirtualKey.Number1 + index), VirtualKeyModifiers.Control, () => ViewModel.ShowSection(index));
        }
    }

    private void Add(VirtualKey key, VirtualKeyModifiers modifiers, Action action)
    {
        var accelerator = new KeyboardAccelerator { Key = key, Modifiers = modifiers };
        accelerator.Invoked += (_, args) =>
        {
            args.Handled = true;
            action();
        };
        KeyboardAccelerators.Add(accelerator);
    }

    /// <summary>F6: the focus goes from the sidebar to the page and back, the way it does between the panes of Explorer.</summary>
    private void MoveToNextPart(bool forward)
    {
        UIElement[] parts = [Sidebar, PageHost];
        var current = parts.ToList().FindIndex(part => FocusManager.GetFocusedElement(XamlRoot) is DependencyObject focused && IsInside(focused, part));
        var next = parts[((current < 0 ? 0 : current) + (forward ? 1 : parts.Length - 1)) % parts.Length];
        if (FocusManager.FindFirstFocusableElement(next) is Control target)
        {
            target.Focus(FocusState.Keyboard);
        }
    }

    private static bool IsInside(DependencyObject element, UIElement ancestor)
    {
        for (var current = element; current is not null; current = Microsoft.UI.Xaml.Media.VisualTreeHelper.GetParent(current))
        {
            if (ReferenceEquals(current, ancestor))
            {
                return true;
            }
        }

        return false;
    }
}
