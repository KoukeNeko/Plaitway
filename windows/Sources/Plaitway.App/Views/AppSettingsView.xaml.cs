using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The app's own settings and the helper.</summary>
internal sealed partial class AppSettingsView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(AppSettingsViewModel), typeof(AppSettingsView), new PropertyMetadata(null, (sender, _) => ((AppSettingsView)sender).Bindings.Update()));

    public AppSettingsView()
    {
        InitializeComponent();
    }

    public AppSettingsViewModel? ViewModel
    {
        get => (AppSettingsViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }
}
