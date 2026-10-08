using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.Presentation;
using Plaitway.AppCore.ViewModels;
using Plaitway.V1;

namespace Plaitway.App.Views;

/// <summary>The routes and DNS tab of a profile.</summary>
internal sealed partial class RoutesView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(RoutesViewModel), typeof(RoutesView), new PropertyMetadata(null, (sender, _) => ((RoutesView)sender).Bindings.Update()));

    public RoutesView()
    {
        InitializeComponent();
    }

    public RoutesViewModel? ViewModel
    {
        get => (RoutesViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    /// <summary>The icon of a route's state; a route that is only named has none.</summary>
    public static StatusIcon IconOf(RouteState? state) => (state ?? RouteState.Unspecified).Icon();

    /// <summary>The colour family of a route's state.</summary>
    public static StatusTone ToneOf(RouteState? state) => (state ?? RouteState.Unspecified).Tone();

    private void OnCopyClick(object sender, RoutedEventArgs args)
    {
        var ids = RouteList.SelectedItems.OfType<RouteRow>().Select(row => row.Id).ToList();
        ViewModel?.CopyPrefixesCommand.Execute(ids);
    }
}
