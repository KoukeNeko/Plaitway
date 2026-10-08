using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The Diagnostics page: what the helper knows, and its own log.</summary>
internal sealed partial class DiagnosticsView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(DiagnosticsViewModel), typeof(DiagnosticsView), new PropertyMetadata(null, (sender, _) => ((DiagnosticsView)sender).Bindings.Update()));

    public DiagnosticsView()
    {
        InitializeComponent();
    }

    public DiagnosticsViewModel? ViewModel
    {
        get => (DiagnosticsViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    // The button is made once per route; the route it belongs to travels with it.
    private void OnRemoveStaleClick(object sender, RoutedEventArgs args)
    {
        if (sender is FrameworkElement { Tag: StaleRouteRow route })
        {
            ViewModel?.RemoveStaleRouteCommand.Execute(route);
        }
    }
}
