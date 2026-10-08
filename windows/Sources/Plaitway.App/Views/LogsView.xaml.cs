using System.ComponentModel;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.ViewModels;
using Plaitway.V1;

namespace Plaitway.App.Views;

/// <summary>The log tab of a profile, and the page of the helper's own log.</summary>
internal sealed partial class LogsView : UserControl
{
    private const double EndTolerance = 8;

    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(LogsViewModel), typeof(LogsView), new PropertyMetadata(null, (sender, args) => ((LogsView)sender).OnViewModelChanged(args)));

    private ScrollViewer? _scroller;

    public LogsView()
    {
        InitializeComponent();
        Loaded += (_, _) => HookScroller();
    }

    public LogsViewModel? ViewModel
    {
        get => (LogsViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    /// <summary>The time of a line, or nothing when the daemon did not say.</summary>
    public static string TimeOf(DateTimeOffset? time) => time is { } value ? Formatting.LogTime(value) : string.Empty;

    /// <summary>The name of a level, the same in every language.</summary>
    public static string TagOf(LogLevel level) => level.Tag();

    /// <summary>The colour family of a level.</summary>
    public static StatusTone ToneOf(LogLevel level) => level.Tone();

    /// <summary>
    /// Takes the user's choice of level. The box also reports "nothing chosen" while its list is being replaced, which is not
    /// a choice, so it must not reach the view model.
    /// </summary>
    private void OnLevelChosen(object sender, SelectionChangedEventArgs args)
    {
        if (ViewModel is { } model && ((ComboBox)sender).SelectedItem is LogFilterChoice choice)
        {
            model.SelectedChoice = choice;
        }
    }

    private void OnViewModelChanged(DependencyPropertyChangedEventArgs args)
    {
        Bindings.Update();
        if (args.OldValue is LogsViewModel old)
        {
            old.PropertyChanged -= OnViewModelPropertyChanged;
        }

        if (args.NewValue is LogsViewModel current)
        {
            current.PropertyChanged += OnViewModelPropertyChanged;
        }
    }

    /// <summary>Follows the newest line while the view is at the end, and takes the focus to the search field when asked.</summary>
    private void OnViewModelPropertyChanged(object? sender, PropertyChangedEventArgs args)
    {
        switch (args.PropertyName)
        {
            case nameof(LogsViewModel.Lines) or nameof(LogsViewModel.IsAtEnd) when ViewModel is { IsAtEnd: true, Lines: { Count: > 0 } lines }:
                DispatcherQueue.TryEnqueue(() => LineList.ScrollIntoView(lines[^1]));
                break;
            case nameof(LogsViewModel.SearchFocusRequest):
                SearchBox.Focus(FocusState.Keyboard);
                break;
            default:
                break;
        }
    }

    private void HookScroller()
    {
        if (_scroller is not null)
        {
            return;
        }

        _scroller = FindScroller(LineList);
        if (_scroller is not null)
        {
            _scroller.ViewChanged += (_, _) =>
            {
                if (ViewModel is { } model)
                {
                    model.IsAtEnd = _scroller.VerticalOffset + _scroller.ViewportHeight >= _scroller.ExtentHeight - EndTolerance;
                }
            };
        }
    }

    private static ScrollViewer? FindScroller(DependencyObject root)
    {
        for (var index = 0; index < VisualTreeHelper.GetChildrenCount(root); index++)
        {
            var child = VisualTreeHelper.GetChild(root, index);
            if (child is ScrollViewer viewer)
            {
                return viewer;
            }

            if (FindScroller(child) is { } found)
            {
                return found;
            }
        }

        return null;
    }
}
