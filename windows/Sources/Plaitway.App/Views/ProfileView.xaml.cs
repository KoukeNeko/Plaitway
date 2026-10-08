using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Plaitway.AppCore.ViewModels;

namespace Plaitway.App.Views;

/// <summary>The page of one profile.</summary>
internal sealed partial class ProfileView : UserControl
{
    public static readonly DependencyProperty ViewModelProperty = DependencyProperty.Register(
        nameof(ViewModel), typeof(ProfilePageViewModel), typeof(ProfileView), new PropertyMetadata(null, (sender, _) => ((ProfileView)sender).Bindings.Update()));

    public ProfileView()
    {
        InitializeComponent();
    }

    public ProfilePageViewModel? ViewModel
    {
        get => (ProfilePageViewModel?)GetValue(ViewModelProperty);
        set => SetValue(ViewModelProperty, value);
    }

    /// <summary>Connecting is the main thing to do, so its button stands out; disconnecting does not.</summary>
    public static Style? ToggleStyle(bool isProminent) =>
        isProminent ? (Style)Application.Current.Resources["AccentButtonStyle"] : (Style)Application.Current.Resources["DefaultButtonStyle"];
}
