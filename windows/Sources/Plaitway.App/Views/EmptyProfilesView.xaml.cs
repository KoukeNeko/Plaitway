using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The window when there is no profile: it offers to import one.</summary>
internal sealed partial class EmptyProfilesView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(EmptyProfilesViewModel), typeof(EmptyProfilesView), new PropertyMetadata(null, (sender, _) => ((EmptyProfilesView)sender).Bindings.Update()));

    public EmptyProfilesView()
    {
        InitializeComponent();
    }

    public EmptyProfilesViewModel? ViewModel
    {
        get => (EmptyProfilesViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }
}
