using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>What the window shows while the helper is not ready.</summary>
internal sealed partial class SetupView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(SetupViewModel), typeof(SetupView), new PropertyMetadata(null, (sender, _) => ((SetupView)sender).Bindings.Update()));

    public SetupView()
    {
        InitializeComponent();
    }

    public SetupViewModel? ViewModel
    {
        get => (SetupViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }
}
